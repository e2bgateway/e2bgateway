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
	"io"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

const (
	logReadTimeout = 10 * time.Second
	logByteLimit   = 1 << 20
)

type logTarget struct {
	sandboxID   string
	claim       *extv1beta1.SandboxClaim
	sandbox     *sandboxv1beta1.Sandbox
	pod         *corev1.Pod
	container   string
	containerID string
}

func (a *Adapter) GetLogs(ctx context.Context, sandboxID string) ([]*adapter.LogEntry, error) {
	return a.GetLogsWithOptions(ctx, sandboxID, adapter.LogOptions{})
}

func (a *Adapter) GetLogsWithOptions(ctx context.Context, sandboxID string, opts adapter.LogOptions) ([]*adapter.LogEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, logReadTimeout)
	defer cancel()
	if a.k8s == nil || a.k8s.ExtensionsClient == nil || a.k8s.AgentsClient == nil || a.k8s.CoreClient == nil {
		return nil, fmt.Errorf("log client unavailable")
	}
	opts, err := normalizeLogOptions(opts)
	if err != nil {
		return nil, err
	}
	target, err := a.resolveLogTarget(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	return a.readPodLogs(ctx, target, opts)
}

func normalizeLogOptions(opts adapter.LogOptions) (adapter.LogOptions, error) {
	if opts.TailLines == 0 {
		opts.TailLines = adapter.DefaultLogTailLines
	}
	if opts.TailLines < 1 || opts.TailLines > adapter.MaxLogTailLines {
		return adapter.LogOptions{}, fmt.Errorf("invalid log tail limit")
	}
	return opts, nil
}

func selectLogContainer(pod *corev1.Pod) (name, containerID string, err error) {
	for _, c := range pod.Spec.Containers {
		if c.Name == "sandbox" || len(pod.Spec.Containers) == 1 {
			name = c.Name
			break
		}
	}
	if name == "" {
		return "", "", fmt.Errorf("sandbox main container cannot be determined")
	}
	containerID = runningLogContainerID(pod, name)
	if containerID == "" {
		return "", "", fmt.Errorf("pod is unscheduled or sandbox container is not running")
	}
	return name, containerID, nil
}

func (a *Adapter) readPodLogs(ctx context.Context, target logTarget, opts adapter.LogOptions) ([]*adapter.LogEntry, error) {
	limit := int64(logByteLimit)
	logOpts := corev1.PodLogOptions{
		Container: target.container, Timestamps: true, TailLines: &opts.TailLines, LimitBytes: &limit,
	}
	if opts.SinceTime != nil {
		since := metav1.NewTime(opts.SinceTime.UTC())
		logOpts.SinceTime = &since
	}
	pods := a.k8s.CoreClient.Pods(a.namespace)
	logs, err := pods.GetLogs(target.pod.Name, &logOpts).Stream(ctx)
	if err != nil {
		// A log 404 can come from kubelet or a missing Node, not just a missing Pod.
		if apierrors.IsNotFound(err) {
			if err := a.recheckLogPod(ctx, target); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("requesting pod logs failed")
	}
	defer logs.Close()
	data, err := io.ReadAll(io.LimitReader(logs, limit))
	if err != nil {
		return nil, fmt.Errorf("reading pod logs failed")
	}
	// Hitting LimitBytes is ambiguous even at a line boundary.
	if len(data) >= logByteLimit {
		return nil, fmt.Errorf("pod logs reached the byte limit")
	}
	// Kubelet can append an error after committing 200. Without a final
	// newline, a late diagnostic may be indistinguishable from a partial log line.
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return nil, fmt.Errorf("pod logs ended without a newline")
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("pod logs are not valid UTF-8")
	}

	// Pod logs are addressed by name only: there is no UID precondition.
	// Recheck the target after the read to reject observed identity changes.
	if err := a.recheckLogTarget(ctx, target); err != nil {
		return nil, err
	}
	return parsePodLogs(data, opts.TailLines, opts.SinceTime, target.container)
}

func parsePodLogs(data []byte, tail int64, since *time.Time, source string) ([]*adapter.LogEntry, error) {
	entries := make([]*adapter.LogEntry, 0)
	for remaining := string(data); remaining != ""; {
		var line string
		line, remaining, _ = strings.Cut(remaining, "\n")
		stamp, message, ok := strings.Cut(line, " ")
		timestamp, err := time.Parse(time.RFC3339Nano, stamp)
		// The RFC3339 parser can accept offsets that JSON cannot encode.
		if err == nil {
			_, err = timestamp.MarshalJSON()
		}
		if !ok || err != nil {
			return nil, fmt.Errorf("pod logs contain an invalid timestamped record")
		}
		// Kubernetes has applied TailLines, but metav1.Time goes across the
		// wire at second precision. Enforce the caller's exact cutoff.
		if since != nil && timestamp.Before(*since) {
			continue
		}
		if int64(len(entries)) >= tail {
			return nil, fmt.Errorf("pod logs exceeded the requested tail limit")
		}
		entries = append(entries, &adapter.LogEntry{Timestamp: timestamp, Message: message, Source: source})
	}
	return entries, nil
}

func runningLogContainerID(pod *corev1.Pod, container string) string {
	if pod.Spec.NodeName != "" {
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == container && status.State.Running != nil {
				return status.ContainerID
			}
		}
	}
	return ""
}

func (a *Adapter) recheckLogPod(ctx context.Context, target logTarget) error {
	current, err := a.k8s.CoreClient.Pods(a.namespace).Get(ctx, target.pod.Name, metav1.GetOptions{})
	if err != nil {
		return logLookupError("rechecking log pod", err)
	}
	if current.UID != target.pod.UID || current.DeletionTimestamp != nil || !sameController(current, target.pod) {
		return adapter.ErrSandboxNotFound
	}
	if runningLogContainerID(current, target.container) != target.containerID {
		return fmt.Errorf("sandbox container changed while reading pod logs")
	}
	return nil
}

func (a *Adapter) recheckLogTarget(ctx context.Context, target logTarget) error {
	if err := a.recheckLogPod(ctx, target); err != nil {
		return err
	}
	claim, err := a.k8s.ExtensionsClient.SandboxClaims(a.namespace).Get(ctx, target.claim.Name, metav1.GetOptions{})
	if err != nil {
		return logLookupError("rechecking log claim", err)
	}
	if claim.UID != target.claim.UID || claim.DeletionTimestamp != nil || claim.Annotations[a.sandboxIDAnnotationKey()] != target.sandboxID {
		return adapter.ErrSandboxNotFound
	}
	sb, err := a.k8s.AgentsClient.Sandboxes(a.namespace).Get(ctx, target.sandbox.Name, metav1.GetOptions{})
	if err != nil {
		return logLookupError("rechecking Sandbox CR for logs", err)
	}
	if sb.UID != target.sandbox.UID || sb.DeletionTimestamp != nil || !controlledBy(sb, claim, extv1beta1.GroupVersion.Group, "SandboxClaim") {
		return adapter.ErrSandboxNotFound
	}
	return nil
}

func (a *Adapter) resolveLogTarget(ctx context.Context, sandboxID string) (logTarget, error) {
	claimName, err := a.resolveClaimName(ctx, sandboxID)
	if err != nil {
		// A failing LIST (even with a 404) says nothing about this sandbox.
		return logTarget{}, fmt.Errorf("sandbox claim lookup failed")
	}
	if len(validation.IsDNS1123Subdomain(claimName)) != 0 {
		return logTarget{}, adapter.ErrSandboxNotFound
	}
	claim, err := a.k8s.ExtensionsClient.SandboxClaims(a.namespace).Get(ctx, claimName, metav1.GetOptions{})
	if err != nil {
		return logTarget{}, logLookupError("getting log claim", err)
	}
	// Cache hits and the lifecycle's historical ID-as-claim-name fallback
	// are not proof of identity. CreateSandbox requires this annotation.
	if claim.DeletionTimestamp != nil || sandboxID == "" || claim.Annotations[a.sandboxIDAnnotationKey()] != sandboxID {
		return logTarget{}, adapter.ErrSandboxNotFound
	}
	sandboxName := claim.Status.SandboxStatus.Name
	if len(validation.IsDNS1123Subdomain(sandboxName)) != 0 {
		return logTarget{}, adapter.ErrSandboxNotFound
	}
	sb, err := a.k8s.AgentsClient.Sandboxes(a.namespace).Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		return logTarget{}, logLookupError("getting Sandbox CR for logs", err)
	}
	if sb.DeletionTimestamp != nil || !controlledBy(sb, claim, extv1beta1.GroupVersion.Group, "SandboxClaim") {
		return logTarget{}, adapter.ErrSandboxNotFound
	}

	pod, err := a.findLogPod(ctx, sb, sb.Annotations[sandboxv1beta1.SandboxPodNameAnnotation])
	if err != nil {
		return logTarget{}, err
	}
	container, containerID, err := selectLogContainer(pod)
	if err != nil {
		return logTarget{}, err
	}
	return logTarget{sandboxID: sandboxID, claim: claim, sandbox: sb, pod: pod, container: container, containerID: containerID}, nil
}

func (a *Adapter) findLogPod(ctx context.Context, sb *sandboxv1beta1.Sandbox, podName string) (*corev1.Pod, error) {
	// Warm-pool adoption may retain a Pod name different from the Sandbox CR.
	if podName == "" {
		podName = sb.Name
	}
	pods := a.k8s.CoreClient.Pods(a.namespace)
	var pod *corev1.Pod
	if len(validation.IsDNS1123Subdomain(podName)) == 0 {
		candidate, err := pods.Get(ctx, podName, metav1.GetOptions{})
		if err == nil && isOwnedLogPod(candidate, sb) {
			pod = candidate
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, logLookupError("getting log pod", err)
		}
	}

	// Check for ambiguity even when a named Pod was found. The label only
	// selects candidates; ownership is established by the Sandbox controller ref.
	selector := sandboxv1beta1.SandboxNameHashLabel + "=" + warmPoolNameHash(sb.Name)
	list, err := pods.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("listing log pods failed")
	}
	for i := range list.Items {
		candidate := &list.Items[i]
		if !isOwnedLogPod(candidate, sb) {
			continue
		}
		if pod == nil {
			pod = candidate
		} else if pod.UID != candidate.UID {
			return nil, fmt.Errorf("multiple pods match the sandbox log target")
		}
	}
	if pod == nil {
		return nil, adapter.ErrSandboxNotFound
	}
	return pod, nil
}

func isOwnedLogPod(pod *corev1.Pod, sb *sandboxv1beta1.Sandbox) bool {
	return pod.UID != "" && pod.DeletionTimestamp == nil && controlledBy(pod, sb, sandboxv1beta1.GroupVersion.Group, "Sandbox")
}

func controlledBy(child, parent metav1.Object, group, kind string) bool {
	ref := metav1.GetControllerOfNoCopy(child)
	if ref == nil || parent.GetUID() == "" || ref.UID != parent.GetUID() || ref.Name != parent.GetName() || ref.Kind != kind {
		return false
	}
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	return err == nil && gv.Group == group
}

func sameController(current, original *corev1.Pod) bool {
	want := metav1.GetControllerOfNoCopy(original)
	got := metav1.GetControllerOfNoCopy(current)
	return got != nil && want.UID == got.UID && want.Name == got.Name && want.Kind == got.Kind && want.APIVersion == got.APIVersion
}

func logLookupError(operation string, err error) error {
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%s: %w", operation, adapter.ErrSandboxNotFound)
	}
	// Kubernetes client errors can include a response body. Do not propagate it.
	return fmt.Errorf("%s failed", operation)
}
