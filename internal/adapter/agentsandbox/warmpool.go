// Copyright The E2BGateway Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agentsandbox

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

// warmPoolTemplateAnnotation records the E2B template ID a pool was created
// for. It gives read paths an unambiguous template identity (reverse-mapping
// templateToSandboxTemplate is not injective) and lets New() restore
// template→pool routing after a gateway restart.
const warmPoolTemplateAnnotation = "e2b.e2bgateway.io/template-id"

// warmPoolCreatedAtAnnotation records the gateway-side creation time in
// RFC3339Nano. K8s serializes metav1.Time at second precision, so pools
// created for one template within the same second would otherwise tie and
// routing restore would degrade to List ordering.
const warmPoolCreatedAtAnnotation = "e2b.e2bgateway.io/created-at"

// warmPoolRestoreTimeout bounds the best-effort SandboxWarmPool listing done
// at adapter construction.
const warmPoolRestoreTimeout = 10 * time.Second

// warmPoolRoutingTTL is how long template→pool routing may be stale before
// resolveWarmPool refreshes it from the CRDs. The gateway runs multiple
// replicas: a pool created or deleted through another replica converges
// within this window without a restart.
const warmPoolRoutingTTL = 30 * time.Second

// warmPoolRoutingRetryBackoff spaces out routing refresh retries after a
// failed attempt. Without it a persistently failing List would leave
// warmPoolRoutingAt stale, making every CreateSandbox that acquires
// warmPoolOpsMu pay the restore timeout synchronously before falling back to
// the (still valid) cached route.
const warmPoolRoutingRetryBackoff = 30 * time.Second

// CreateWarmPool creates a SandboxWarmPool CRD in the adapter namespace. The
// agent-sandbox controller reconciles the pool to the requested replica
// count; sandboxes are claimed from it via SandboxClaim WarmPoolRef.
func (a *Adapter) CreateWarmPool(ctx context.Context, req *adapter.WarmPoolCreateRequest) (*adapter.WarmPool, error) {
	// Serialize with DeleteWarmPool so the CRD write and the warmPoolMap
	// publication below cannot be interleaved by a concurrent delete.
	a.warmPoolOpsMu.Lock()
	defer a.warmPoolOpsMu.Unlock()

	if req == nil || req.TemplateID == "" {
		return nil, fmt.Errorf("templateID is required")
	}
	size, err := adapter.ClampTargetSize(req.TargetSize, a.maxWarmPoolSize)
	if err != nil {
		return nil, err
	}
	id := req.WarmPoolID
	if id == "" {
		id = adapter.GenerateWarmPoolID(req.TemplateID)
	}
	if err := adapter.ValidateWarmPoolID(id); err != nil {
		return nil, err
	}

	// Validate the referenced SandboxTemplate exists so the controller does
	// not sit on an unresolvable ref.
	ref := a.resolveTemplateRef(req.TemplateID)
	if _, err := a.k8s.ExtensionsClient.SandboxTemplates(a.namespace).Get(ctx, ref, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("SandboxTemplate %q not found in namespace %q: create it before the warm pool", ref, a.namespace)
		}
		return nil, fmt.Errorf("checking SandboxTemplate %q: %w", ref, err)
	}

	replicas := int32(size)
	swp := &extv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      id,
			Namespace: a.namespace,
			Annotations: map[string]string{
				warmPoolTemplateAnnotation:  req.TemplateID,
				warmPoolCreatedAtAnnotation: time.Now().UTC().Format(time.RFC3339Nano),
			},
		},
		Spec: extv1beta1.SandboxWarmPoolSpec{
			Replicas:    &replicas,
			TemplateRef: extv1beta1.SandboxTemplateRef{Name: ref},
		},
	}
	if a.warmPoolUpdateStrategy != "" {
		swp.Spec.UpdateStrategy = &extv1beta1.SandboxWarmPoolUpdateStrategy{
			Type: extv1beta1.SandboxWarmPoolUpdateStrategyType(a.warmPoolUpdateStrategy),
		}
	}
	created, err := a.k8s.ExtensionsClient.SandboxWarmPools(a.namespace).Create(ctx, swp, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil, adapter.ErrWarmPoolAlreadyExists
		}
		return nil, fmt.Errorf("creating SandboxWarmPool %q: %w", id, err)
	}

	// Make the new pool immediately consumable by CreateSandbox on this
	// replica without a restart (other replicas converge via the TTL
	// refresh). Config-provided routes are explicit operator overrides and
	// are never displaced by CRUD-created pools.
	if _, fromConfig := a.warmPoolConfig[req.TemplateID]; !fromConfig {
		a.warmPoolMapMu.Lock()
		a.warmPoolMap[req.TemplateID] = id
		a.warmPoolMapMu.Unlock()
	}

	wp := a.toAdapterWarmPool(created)
	wp.TemplateID = req.TemplateID // E2B-side ID, not the CRD ref name
	return wp, nil
}

// GetWarmPool reads a SandboxWarmPool CRD by name.
func (a *Adapter) GetWarmPool(ctx context.Context, warmPoolID string) (*adapter.WarmPool, error) {
	swp, err := a.k8s.ExtensionsClient.SandboxWarmPools(a.namespace).Get(ctx, warmPoolID, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, adapter.ErrWarmPoolNotFound
		}
		return nil, fmt.Errorf("getting SandboxWarmPool %q: %w", warmPoolID, err)
	}
	return a.toAdapterWarmPool(swp), nil
}

// ListWarmPools lists all SandboxWarmPool CRDs in the adapter namespace.
func (a *Adapter) ListWarmPools(ctx context.Context) ([]*adapter.WarmPool, error) {
	list, err := a.k8s.ExtensionsClient.SandboxWarmPools(a.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing SandboxWarmPools: %w", err)
	}
	pools := make([]*adapter.WarmPool, 0, len(list.Items))
	for i := range list.Items {
		pools = append(pools, a.toAdapterWarmPool(&list.Items[i]))
	}
	return pools, nil
}

// DeleteWarmPool deletes the SandboxWarmPool CRD. The controller reclaims
// unclaimed sandboxes per its updateStrategy; claimed sandboxes are untouched.
func (a *Adapter) DeleteWarmPool(ctx context.Context, warmPoolID string) error {
	// Serialize with CreateWarmPool: see warmPoolOpsMu.
	a.warmPoolOpsMu.Lock()
	defer a.warmPoolOpsMu.Unlock()

	err := a.k8s.ExtensionsClient.SandboxWarmPools(a.namespace).Delete(ctx, warmPoolID, metav1.DeleteOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return adapter.ErrWarmPoolNotFound
		}
		return fmt.Errorf("deleting SandboxWarmPool %q: %w", warmPoolID, err)
	}
	// Drop cached routes to the deleted pool BEFORE the best-effort refresh:
	// if the List below fails, the cache must not keep pointing claims at a
	// pool that no longer exists (the controller would answer
	// WarmPoolNotFound). Config overrides for affected templates are
	// restored by the next successful refresh. warmPoolOpsMu is already
	// held, so this cannot interleave with a concurrent create's
	// publication.
	a.warmPoolMapMu.Lock()
	for tmpl, wp := range a.warmPoolMap {
		if wp == warmPoolID {
			delete(a.warmPoolMap, tmpl)
		}
	}
	a.warmPoolMapMu.Unlock()
	// Rebuild routing from the remaining CRDs instead of just dropping
	// entries that point at the deleted pool: another annotated pool may
	// still serve the same template (deleting the newest must re-route to
	// the survivor, not fall back to the raw template ID), and config
	// overrides are restored for templates whose only pool was deleted.
	// warmPoolOpsMu is already held, so this cannot interleave with a
	// concurrent create's publication.
	a.refreshWarmPoolRouting(ctx)
	return nil
}

// UpdateWarmPoolSize patches spec.replicas; the controller reconciles.
func (a *Adapter) UpdateWarmPoolSize(ctx context.Context, warmPoolID string, targetSize int) error {
	size, err := adapter.ClampTargetSize(targetSize, a.maxWarmPoolSize)
	if err != nil {
		return err
	}
	patch := fmt.Appendf(nil, `{"spec":{"replicas":%d}}`, size)
	_, err = a.k8s.ExtensionsClient.SandboxWarmPools(a.namespace).Patch(
		ctx, warmPoolID, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return adapter.ErrWarmPoolNotFound
		}
		return fmt.Errorf("patching SandboxWarmPool %q: %w", warmPoolID, err)
	}
	return nil
}

// resolveTemplateRef maps an E2B template ID to a SandboxTemplate name.
func (a *Adapter) resolveTemplateRef(templateID string) string {
	if ref, ok := a.templateToSandboxTemplate[templateID]; ok {
		return ref
	}
	// Viper lowercases map keys when loading config, so a mapping configured
	// for "MyTemplate" is stored under "mytemplate". Retry with the lowercased
	// ID before falling back to the ID itself.
	if ref, ok := a.templateToSandboxTemplate[strings.ToLower(templateID)]; ok {
		return ref
	}
	return templateID
}

// refreshWarmPoolRouting rebuilds template→pool routing from the config
// overrides plus the persisted SandboxWarmPool CRDs annotated by
// CreateWarmPool. It runs at startup (so CRUD-created pools survive gateway
// restarts), on TTL expiry inside resolveWarmPool (so pools created or
// deleted through OTHER gateway replicas converge without a restart), and
// after DeleteWarmPool (so routing falls back to a surviving pool).
// Best-effort: on listing failure the current routing is kept.
func (a *Adapter) refreshWarmPoolRouting(ctx context.Context) {
	// Record the attempt (not just successes) so resolveWarmPool can back off
	// after failures. Successes additionally bump warmPoolRoutingAt below.
	a.warmPoolMapMu.Lock()
	a.warmPoolRoutingLastAttempt = time.Now()
	a.warmPoolMapMu.Unlock()

	list, err := a.k8s.ExtensionsClient.SandboxWarmPools(a.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		slog.Warn("warm pool routing refresh failed; keeping current routing",
			"adapter", a.name, "namespace", a.namespace, "error", err,
			"nextRetryIn", warmPoolRoutingRetryBackoff.String())
		return
	}
	type candidate struct {
		name    string
		created time.Time
	}
	best := make(map[string]candidate)
	for i := range list.Items {
		item := &list.Items[i]
		tmpl := item.Annotations[warmPoolTemplateAnnotation]
		if tmpl == "" {
			// Operator-managed pool without E2B template identity: consumed
			// only via explicit warmPoolName/templateToWarmPool config.
			continue
		}
		created := warmPoolCreatedAt(item)
		if prev, seen := best[tmpl]; seen {
			// Several CRUD-created pools share this template ID (e.g. an old
			// zero-sized pool plus a newer one): keep the most recently
			// created so routing does not depend on List ordering. Exact
			// ties (same nanosecond stamp, or both falling back to the
			// second-precise CreationTimestamp) break by name so the result
			// is still deterministic.
			if created.Before(prev.created) || (created.Equal(prev.created) && item.Name <= prev.name) {
				continue
			}
		}
		best[tmpl] = candidate{name: item.Name, created: created}
	}

	newMap := make(map[string]string, len(a.warmPoolConfig)+len(best))
	maps.Copy(newMap, a.warmPoolConfig) // config entries are operator overrides and win
	for tmpl, c := range best {
		if _, fromConfig := newMap[tmpl]; !fromConfig {
			newMap[tmpl] = c.name
		}
	}

	a.warmPoolMapMu.Lock()
	a.warmPoolMap = newMap
	a.warmPoolRoutingAt = time.Now()
	a.warmPoolMapMu.Unlock()
}

// warmPoolCreatedAt returns the gateway-stamped creation time (RFC3339Nano)
// recorded by CreateWarmPool, falling back to the K8s CreationTimestamp —
// which is only second-precise — for pools created before the stamp existed.
func warmPoolCreatedAt(swp *extv1beta1.SandboxWarmPool) time.Time {
	if s := swp.Annotations[warmPoolCreatedAtAnnotation]; s != "" {
		if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return ts
		}
	}
	return swp.CreationTimestamp.Time
}

// templateIDForRef reverses resolveTemplateRef for read paths. Falls back to
// the ref name when no mapping matches.
func (a *Adapter) templateIDForRef(ref string) string {
	for tmpl, r := range a.templateToSandboxTemplate {
		if r == ref {
			return tmpl
		}
	}
	return ref
}

// toAdapterWarmPool converts the CRD to the gateway DTO.
func (a *Adapter) toAdapterWarmPool(swp *extv1beta1.SandboxWarmPool) *adapter.WarmPool {
	target := 0
	if swp.Spec.Replicas != nil {
		target = int(*swp.Spec.Replicas)
	}
	// Prefer the E2B template ID recorded at creation: reverse-mapping the
	// TemplateRef is ambiguous when several template IDs share one ref.
	templateID := swp.Annotations[warmPoolTemplateAnnotation]
	if templateID == "" {
		templateID = a.templateIDForRef(swp.Spec.TemplateRef.Name)
	}
	return &adapter.WarmPool{
		WarmPoolID:  swp.Name,
		TemplateID:  templateID,
		TargetSize:  target,
		CurrentSize: int(swp.Status.ReadyReplicas),
		Status:      warmPoolStatus(swp),
		CreatedAt:   swp.CreationTimestamp.Time,
	}
}

// warmPoolStatus derives the unified status vocabulary (pending / scaling /
// ready) from CRD spec/status fields.
func warmPoolStatus(swp *extv1beta1.SandboxWarmPool) string {
	target := int32(0)
	if swp.Spec.Replicas != nil {
		target = *swp.Spec.Replicas
	}
	switch {
	// Check observation lag first: a pool just resized (including to zero)
	// whose status the controller has not caught up with is still pending,
	// not ready.
	case swp.Status.ObservedGeneration < swp.Generation:
		return adapter.WarmPoolStatusPending
	case target == 0:
		return adapter.WarmPoolStatusReady
	case swp.Status.ReadyReplicas >= target:
		return adapter.WarmPoolStatusReady
	case swp.Status.ReadyReplicas > 0:
		return adapter.WarmPoolStatusScaling
	default:
		return adapter.WarmPoolStatusPending
	}
}
