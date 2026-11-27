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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"github.com/e2bgateway/e2bgateway/internal/adapter"
)

const (
	logsNamespace = "gateway-sandboxes"
	logsSandboxID = "public-sandbox-id"
	logsTimestamp = "2026-11-19T12:34:56.123456789Z"
	logsNameHash  = "7873761e" // Agent-Sandbox v1.0.11 NameHash("sandbox-cr").
)

func TestGetLogsReadsOwnedPod(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "adopted warm pool pod"
		if legacy {
			name = "pod named after Sandbox CR and sole container"
		}
		t.Run(name, func(t *testing.T) {
			claim, sb, pod := logObjects()
			container := "sandbox"
			if legacy {
				delete(sb.Annotations, sandboxv1beta1.SandboxPodNameAnnotation)
				pod.Name = sb.Name
				container = "main"
				pod.Spec.Containers = []corev1.Container{{Name: container}}
				pod.Status.ContainerStatuses = pod.Status.ContainerStatuses[1:]
				pod.Status.ContainerStatuses[0].Name = container
				// Older identity whose public ID is also the claim name.
				claim.Name = logsSandboxID
				sb.OwnerReferences[0].Name = claim.Name
			}
			// The same Pod found by name and by label counts only once.
			replies := logPodReplies{listed: []corev1.Pod{*pod}}
			a, calls := newLogTestAdapter(t, claim, sb, pod, replies, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, url.Values{
					"container": {container}, "timestamps": {"true"}, "tailLines": {"100"}, "limitBytes": {"1048576"},
				}, r.URL.Query())
				_, _ = io.WriteString(w, logsTimestamp+"  ERROR: message  \r\n"+logsTimestamp+" \n")
			})
			logs, err := a.GetLogs(t.Context(), logsSandboxID)
			require.NoError(t, err)
			require.Len(t, logs, 2)
			assert.Equal(t, logsTimestamp, logs[0].Timestamp.Format(time.RFC3339Nano))
			assert.Equal(t, " ERROR: message  \r", logs[0].Message)
			assert.Equal(t, "", logs[1].Message)
			assert.Equal(t, container, logs[0].Source)
			assert.Empty(t, logs[0].Level)
			assert.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestGetLogsRejectsUnverifiedObjects(t *testing.T) {
	for _, name := range []string{"missing claim", "unannotated name fallback", "mismatching claim annotation", "terminating claim", "unbound claim", "missing Sandbox CR", "terminating Sandbox CR", "stale claim owner UID", "missing pod", "terminating pod", "stale Sandbox owner UID", "ambiguous containers"} {
		t.Run(name, func(t *testing.T) {
			claim, sb, pod := logObjects()
			switch name {
			case "unannotated name fallback":
				claim.Name, claim.Annotations = logsSandboxID, nil
				sb.OwnerReferences[0].Name = claim.Name
			case "mismatching claim annotation":
				claim.Annotations[defaultSandboxIDAnnotation] = "someone-else"
			case "terminating claim":
				claim.DeletionTimestamp = &metav1.Time{Time: time.Now()}
			case "unbound claim":
				claim.Status.SandboxStatus.Name = ""
			case "missing Sandbox CR":
				claim.Status.SandboxStatus.Name = "missing-sandbox"
			case "terminating Sandbox CR":
				sb.DeletionTimestamp = &metav1.Time{Time: time.Now()}
			case "stale claim owner UID":
				sb.OwnerReferences[0].UID = "old-claim"
			case "missing pod":
				sb.Annotations[sandboxv1beta1.SandboxPodNameAnnotation] = "missing-pod"
			case "terminating pod":
				pod.DeletionTimestamp = &metav1.Time{Time: time.Now()}
			case "stale Sandbox owner UID":
				pod.OwnerReferences[0].UID = "old-sandbox"
			case "ambiguous containers":
				pod.Spec.Containers[1].Name = "worker"
				pod.Spec.InitContainers = []corev1.Container{{Name: "sandbox"}}
			}
			a, calls := newLogTestAdapter(t, claim, sb, pod, logPodReplies{}, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, logsTimestamp+" unrelated logs\n")
			})
			if name == "mismatching claim annotation" {
				a.idMap = map[string]*sandboxEntry{logsSandboxID: {claimName: claim.Name}}
			}
			id := logsSandboxID
			if name == "missing claim" {
				id = "missing-claim"
			}
			logs, err := a.GetLogs(t.Context(), id)
			require.Error(t, err)
			if name == "ambiguous containers" {
				assert.NotErrorIs(t, err, adapter.ErrSandboxNotFound)
			} else {
				assert.ErrorIs(t, err, adapter.ErrSandboxNotFound)
			}
			assert.Nil(t, logs)
			assert.Zero(t, calls.Load())
		})
	}
}

func TestGetLogsFindsWarmPodByTrackingLabel(t *testing.T) {
	for _, tt := range []struct {
		name, hint string
	}{
		{"annotation cleared and default name missing", ""},
		{"stale Pod name", "missing-pod"},
		{"invalid hint", "../invalid"},
		{"hint names a Pod owned by a different Sandbox UID", "foreign-pod"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claim, sb, pod := logObjects()
			sb.Annotations[sandboxv1beta1.SandboxPodNameAnnotation] = tt.hint
			foreign := pod.DeepCopy()
			foreign.Name, foreign.UID = "foreign-pod", "foreign-pod-uid"
			foreign.OwnerReferences[0].UID = "old-sandbox-uid"
			terminating := pod.DeepCopy()
			terminating.Name, terminating.UID = "terminating-pod", "terminating-pod-uid"
			terminating.DeletionTimestamp = &metav1.Time{Time: time.Now()}
			replies := logPodReplies{other: foreign, listed: []corev1.Pod{*foreign, *terminating, *pod}}
			a, calls := newLogTestAdapter(t, claim, sb, pod, replies, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, logsTimestamp+" warm logs\n")
			})
			logs, err := a.GetLogs(t.Context(), logsSandboxID)
			require.NoError(t, err)
			require.Len(t, logs, 1)
			assert.Equal(t, "warm logs", logs[0].Message)
			assert.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestGetLogsFailsClosedOnUnusableTrackingResults(t *testing.T) {
	const secret = "sensitive upstream payload"
	resource := schema.GroupResource{Resource: "pods"}
	for _, tt := range []struct {
		name                string
		getError, listError *apierrors.StatusError
		secondOwnerUID      types.UID
		named               bool
		missing             bool
	}{
		{name: "only label match has a foreign UID", secondOwnerUID: "old-sandbox-uid", missing: true},
		{name: "two nonterminating owned Pods", secondOwnerUID: "sandbox-uid"},
		{name: "named Pod and a different owned label match", secondOwnerUID: "sandbox-uid", named: true},
		{name: "list 404 is not object absence", listError: apierrors.NewNotFound(resource, secret)},
		{name: "list forbidden", listError: apierrors.NewForbidden(resource, secret, nil)},
		{name: "list forbidden with a valid named Pod", listError: apierrors.NewForbidden(resource, secret, nil), named: true},
		{name: "named GET forbidden", getError: apierrors.NewForbidden(resource, secret, nil)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claim, sb, pod := logObjects()
			if !tt.named {
				sb.Annotations[sandboxv1beta1.SandboxPodNameAnnotation] = "missing-pod"
			}
			replies := logPodReplies{getError: tt.getError, listError: tt.listError}
			if tt.secondOwnerUID != "" {
				second := pod.DeepCopy()
				second.Name, second.UID = "second-pod", "second-pod-uid"
				second.OwnerReferences[0].UID = tt.secondOwnerUID
				if !tt.missing && !tt.named {
					replies.listed = append(replies.listed, *pod)
				}
				replies.listed = append(replies.listed, *second)
			}
			a, calls := newLogTestAdapter(t, claim, sb, pod, replies, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, logsTimestamp+" unrelated logs\n")
			})
			logs, err := a.GetLogs(t.Context(), logsSandboxID)
			require.Error(t, err)
			if tt.missing {
				assert.ErrorIs(t, err, adapter.ErrSandboxNotFound)
			} else {
				assert.NotErrorIs(t, err, adapter.ErrSandboxNotFound)
			}
			if !tt.missing && tt.secondOwnerUID != "" {
				assert.EqualError(t, err, "multiple pods match the sandbox log target")
			}
			assert.NotContains(t, err.Error(), secret)
			assert.Nil(t, logs)
			assert.Zero(t, calls.Load())
		})
	}
}

func TestGetLogsRejectsInvalidUpstreamData(t *testing.T) {
	const syntheticLogPath = "/var/log/pods/synthetic-sandbox/sandbox/0.log"
	for _, tt := range []struct {
		name      string
		status    int
		body      string
		shortRead bool
	}{
		{"log request forbidden", http.StatusForbidden, "sensitive upstream payload", false},
		{"empty record followed by data", 200, "\n" + logsTimestamp + " valid\n", false},
		{"malformed final timestamp after valid line", 200, logsTimestamp + " valid\nsensitive upstream payload\n", false},
		{"JSON cannot encode final timestamp", 200, logsTimestamp + " valid\n2026-11-19T12:34:56+24:00 unencodable timestamp\n", false},
		{"invalid UTF-8 in final record", 200, logsTimestamp + " \xff\n", false},
		{"valid partial final record", 200, logsTimestamp + " final fragment", false},
		{"late diagnostic appended to partial record", 200, logsTimestamp + " complete line\n" + logsTimestamp + " final fragment" + "read " + syntheticLogPath + ": input/output error", false},
		{"exactly at cap with newline", 200, logsTimestamp + " " + strings.Repeat("x", (1<<20)-len(logsTimestamp)-2) + "\n", false},
		{"over cap", 200, logsTimestamp + " " + strings.Repeat("x", 1<<20), false},
		{"unexpected EOF in transport", 200, logsTimestamp + " complete line\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claim, sb, pod := logObjects()
			a, calls := newLogTestAdapter(t, claim, sb, pod, logPodReplies{}, func(w http.ResponseWriter, _ *http.Request) {
				if tt.shortRead {
					w.Header().Set("Content-Length", strconv.Itoa(len(tt.body)+1))
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			logs, err := a.GetLogs(t.Context(), logsSandboxID)
			require.Error(t, err)
			assert.NotErrorIs(t, err, adapter.ErrSandboxNotFound)
			assert.NotContains(t, err.Error(), "sensitive upstream payload")
			assert.NotContains(t, err.Error(), syntheticLogPath)
			assert.Nil(t, logs)
			assert.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestGetLogsAcceptsEmptyOrCompleteSnapshot(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		messages   []string
	}{
		{"genuinely empty body", "", []string{}},
		{"newline-terminated records", logsTimestamp + " a full line\n" + logsTimestamp + " final line\n", []string{"a full line", "final line"}},
		{"timestamp with extra fractional digits", "2026-11-19T12:34:56.123456789123Z log line\n", []string{"log line"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claim, sb, pod := logObjects()
			a, calls := newLogTestAdapter(t, claim, sb, pod, logPodReplies{}, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			})
			logs, err := a.GetLogs(t.Context(), logsSandboxID)
			require.NoError(t, err)
			require.NotNil(t, logs)
			require.Len(t, logs, len(tt.messages))
			for i, message := range tt.messages {
				assert.Equal(t, message, logs[i].Message)
			}
			assert.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestGetLogsRequiresScheduledRunningContainer(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*corev1.Pod)
		status int
	}{
		{"unscheduled", func(p *corev1.Pod) { p.Spec.NodeName = "" }, http.StatusNoContent},
		{"selected container status absent", func(p *corev1.Pod) { p.Status.ContainerStatuses = p.Status.ContainerStatuses[:1] }, http.StatusOK},
		{"running but no container ID", func(p *corev1.Pod) { p.Status.ContainerStatuses[1].ContainerID = "" }, http.StatusOK},
		{"waiting after termination", func(p *corev1.Pod) {
			status := &p.Status.ContainerStatuses[1]
			status.State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}
			status.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ContainerID: status.ContainerID}
		}, http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claim, sb, pod := logObjects()
			tt.change(pod)
			a, calls := newLogTestAdapter(t, claim, sb, pod, logPodReplies{}, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			})
			logs, err := a.GetLogs(t.Context(), logsSandboxID)
			require.Error(t, err)
			assert.NotErrorIs(t, err, adapter.ErrSandboxNotFound)
			assert.Nil(t, logs)
			assert.Zero(t, calls.Load())
		})
	}
}

func TestGetLogsRechecksPodAndContainer(t *testing.T) {
	for _, tt := range []struct {
		name    string
		change  func(*corev1.Pod)
		missing bool
	}{
		{"new pod UID", func(p *corev1.Pod) { p.UID = "new-pod-uid" }, true},
		{"changed owner", func(p *corev1.Pod) { p.OwnerReferences[0].UID = "new-owner-uid" }, true},
		{"unscheduled", func(p *corev1.Pod) { p.Spec.NodeName = "" }, false},
		{"container stopped", func(p *corev1.Pod) {
			p.Status.ContainerStatuses[1].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}
		}, false},
		{"container restarted", func(p *corev1.Pod) { p.Status.ContainerStatuses[1].ContainerID = "containerd://next-attempt" }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claim, sb, pod := logObjects()
			replies := logPodReplies{afterRead: func(p *corev1.Pod) *corev1.Pod { tt.change(p); return p }}
			a, calls := newLogTestAdapter(t, claim, sb, pod, replies, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, logsTimestamp+" log during change\n")
			})
			logs, err := a.GetLogs(t.Context(), logsSandboxID)
			require.Error(t, err)
			if tt.missing {
				assert.ErrorIs(t, err, adapter.ErrSandboxNotFound)
			} else {
				assert.NotErrorIs(t, err, adapter.ErrSandboxNotFound)
			}
			assert.Nil(t, logs)
			assert.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestGetLogsRechecksClaimAndSandbox(t *testing.T) {
	const secret = "sensitive upstream payload"
	terminate := func(obj metav1.Object) {
		now := metav1.Now()
		obj.SetDeletionTimestamp(&now)
	}
	for _, tt := range []struct {
		name, resource string
		change         func(metav1.Object)
		status         int
	}{
		{name: "claim removed", resource: "sandboxclaims", status: http.StatusNotFound},
		{name: "claim replaced", resource: "sandboxclaims", change: func(obj metav1.Object) { obj.SetUID("new-claim-uid") }},
		{name: "claim terminating", resource: "sandboxclaims", change: terminate},
		{name: "claim annotation changed", resource: "sandboxclaims", change: func(obj metav1.Object) {
			obj.SetAnnotations(map[string]string{defaultSandboxIDAnnotation: "different-sandbox-id"})
		}},
		{name: "claim recheck forbidden", resource: "sandboxclaims", status: http.StatusForbidden},
		{name: "Sandbox removed", resource: "sandboxes", status: http.StatusNotFound},
		{name: "Sandbox replaced", resource: "sandboxes", change: func(obj metav1.Object) { obj.SetUID("new-sandbox-uid") }},
		{name: "Sandbox terminating", resource: "sandboxes", change: terminate},
		{name: "Sandbox no longer owned by claim", resource: "sandboxes", change: func(obj metav1.Object) { obj.SetOwnerReferences(nil) }},
		{name: "Sandbox recheck forbidden", resource: "sandboxes", status: http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claim, sb, pod := logObjects()
			a, claims, sandboxes := newWarmPoolForceTestAdapter(t, []runtime.Object{claim}, sb)
			calls := serveLogTestPod(t, a, pod, logPodReplies{}, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, logsTimestamp+" log during change\n")
			})
			client, original := &claims.Fake, runtime.Object(claim)
			if tt.resource == "sandboxes" {
				client, original = &sandboxes.Fake, sb
			}
			client.PrependReactor("get", tt.resource, func(action k8stesting.Action) (bool, runtime.Object, error) {
				if calls.Load() == 0 {
					return false, nil, nil
				}
				resource := action.GetResource().GroupResource()
				switch tt.status {
				case http.StatusNotFound:
					return true, nil, apierrors.NewNotFound(resource, secret)
				case http.StatusForbidden:
					return true, nil, apierrors.NewForbidden(resource, secret, nil)
				}
				current := original.DeepCopyObject()
				tt.change(current.(metav1.Object))
				return true, current, nil
			})

			logs, err := a.GetLogs(t.Context(), logsSandboxID)
			require.Error(t, err)
			if tt.status == http.StatusForbidden {
				assert.NotErrorIs(t, err, adapter.ErrSandboxNotFound)
			} else {
				assert.ErrorIs(t, err, adapter.ErrSandboxNotFound)
			}
			assert.NotContains(t, err.Error(), secret)
			assert.Nil(t, logs)
			assert.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestGetLogsDistinguishesLogNotFound(t *testing.T) {
	for _, tt := range []struct {
		name     string
		resource string
		vanished bool
	}{
		{"typed Pod missing", "pods", true},
		{"typed Node missing", "nodes", false},
		{"untyped 404 with same Pod", "", false},
		{"untyped 404 with vanished Pod", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claim, sb, pod := logObjects()
			replies := logPodReplies{}
			if tt.vanished {
				replies.afterRead = func(*corev1.Pod) *corev1.Pod { return nil }
			}
			a, calls := newLogTestAdapter(t, claim, sb, pod, replies, func(w http.ResponseWriter, _ *http.Request) {
				switch tt.resource {
				case "pods":
					writeLogTestStatus(w, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, pod.Name))
				case "nodes":
					writeLogTestStatus(w, apierrors.NewNotFound(schema.GroupResource{Resource: "nodes"}, pod.Spec.NodeName))
				default:
					http.Error(w, "sensitive upstream payload", http.StatusNotFound)
				}
			})
			logs, err := a.GetLogs(t.Context(), logsSandboxID)
			require.Error(t, err)
			if tt.vanished {
				assert.ErrorIs(t, err, adapter.ErrSandboxNotFound)
			} else {
				assert.NotErrorIs(t, err, adapter.ErrSandboxNotFound)
			}
			assert.NotContains(t, err.Error(), pod.Name)
			assert.NotContains(t, err.Error(), pod.Spec.NodeName)
			assert.NotContains(t, err.Error(), "sensitive upstream payload")
			assert.Nil(t, logs)
			assert.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestGetLogsPropagatesClaimLookupFailure(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			a, cs, _ := newWarmPoolForceTestAdapter(t, nil)
			core, err := typedcorev1.NewForConfig(&rest.Config{Host: "http://unused.invalid"})
			require.NoError(t, err)
			a.k8s.CoreClient = core
			cs.PrependReactor("list", "sandboxclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{Code: int32(status)}}
			})
			logs, err := a.GetLogs(t.Context(), logsSandboxID)
			require.Error(t, err)
			assert.NotErrorIs(t, err, adapter.ErrSandboxNotFound)
			assert.Nil(t, logs)
		})
	}
}

func TestGetLogsWithOptionsPreservesSinceTimePrecision(t *testing.T) {
	for _, tt := range []struct {
		name, cutoff, wireTime string
		messages               []string
	}{
		{"fractional cutoff with offset", "2026-11-19T13:34:56.12+01:00", "2026-11-19T12:34:56Z", []string{"newest record"}},
		{"record exactly at cutoff is included", logsTimestamp, "2026-11-19T12:34:56Z", []string{"newest record"}},
		{"future cutoff returns empty logs", "2099-01-01T00:00:00Z", "2099-01-01T00:00:00Z", []string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claim, sb, pod := logObjects()
			cutoff, err := time.Parse(time.RFC3339Nano, tt.cutoff)
			require.NoError(t, err)
			a, calls := newLogTestAdapter(t, claim, sb, pod, logPodReplies{}, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, url.Values{
					"container": {"sandbox"}, "timestamps": {"true"}, "tailLines": {"2"}, "limitBytes": {"1048576"},
					"sinceTime": {tt.wireTime},
				}, r.URL.Query())
				_, _ = io.WriteString(w, "2026-11-19T12:34:56.1Z older record\n"+logsTimestamp+" newest record\n")
			})
			logs, err := a.GetLogsWithOptions(t.Context(), logsSandboxID, adapter.LogOptions{TailLines: 2, SinceTime: &cutoff})
			require.NoError(t, err)
			require.NotNil(t, logs)
			require.Len(t, logs, len(tt.messages))
			for i, message := range tt.messages {
				assert.Equal(t, message, logs[i].Message)
			}
			assert.EqualValues(t, 1, calls.Load())
		})
	}
}

func logObjects() (*extv1beta1.SandboxClaim, *sandboxv1beta1.Sandbox, *corev1.Pod) {
	claim := annotatedClaim("sandbox-claim", logsSandboxID)
	claim.Namespace, claim.UID = logsNamespace, "claim-uid"
	claim.Status.SandboxStatus.Name = "sandbox-cr"
	// The controller re-parents warm pool Sandboxes to the claim and can keep
	// the original Pod name. The Pod is still owned by the Sandbox CR.
	sb := &sandboxv1beta1.Sandbox{ObjectMeta: metav1.ObjectMeta{
		Namespace: logsNamespace, UID: "sandbox-uid", Name: claim.Status.SandboxStatus.Name,
		Annotations:     map[string]string{sandboxv1beta1.SandboxPodNameAnnotation: "warm-pod"},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(claim, extv1beta1.GroupVersion.WithKind("SandboxClaim"))},
	}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: logsNamespace, UID: "pod-uid", Name: "warm-pod",
			Labels:          map[string]string{sandboxv1beta1.SandboxNameHashLabel: logsNameHash},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sb, sandboxv1beta1.GroupVersion.WithKind("Sandbox"))},
		},
		Spec: corev1.PodSpec{NodeName: "upstream-node", Containers: []corev1.Container{{Name: "sidecar"}, {Name: "sandbox"}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "sidecar", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}},
			{Name: "sandbox", ContainerID: "containerd://first-attempt", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		}},
	}
	return claim, sb, pod
}

type logPodReplies struct {
	afterRead           func(*corev1.Pod) *corev1.Pod
	other               *corev1.Pod
	listed              []corev1.Pod
	getError, listError *apierrors.StatusError
}

func newLogTestAdapter(t *testing.T, claim *extv1beta1.SandboxClaim, sb *sandboxv1beta1.Sandbox, pod *corev1.Pod, replies logPodReplies, serveLogs http.HandlerFunc) (*Adapter, *atomic.Int32) {
	t.Helper()
	a, _, _ := newWarmPoolForceTestAdapter(t, []runtime.Object{claim}, sb)
	return a, serveLogTestPod(t, a, pod, replies, serveLogs)
}

func serveLogTestPod(t *testing.T, a *Adapter, pod *corev1.Pod, replies logPodReplies, serveLogs http.HandlerFunc) *atomic.Int32 {
	t.Helper()
	a.namespace = logsNamespace
	a.idMap = make(map[string]*sandboxEntry)
	var calls atomic.Int32
	base := "/api/v1/namespaces/" + logsNamespace + "/pods/"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		switch {
		case r.URL.Path == strings.TrimSuffix(base, "/"):
			assert.Equal(t, sandboxv1beta1.SandboxNameHashLabel+"="+logsNameHash, r.URL.Query().Get("labelSelector"))
			if replies.listError != nil {
				writeLogTestStatus(w, replies.listError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&corev1.PodList{Items: replies.listed})
		case r.URL.Path == base+pod.Name+"/log":
			calls.Add(1)
			serveLogs(w, r)
		case strings.HasPrefix(r.URL.Path, base) && !strings.Contains(strings.TrimPrefix(r.URL.Path, base), "/"):
			name := strings.TrimPrefix(r.URL.Path, base)
			if replies.getError != nil {
				writeLogTestStatus(w, replies.getError)
				return
			}
			var served *corev1.Pod
			if name == pod.Name {
				served = pod.DeepCopy()
				if replies.afterRead != nil && calls.Load() > 0 {
					served = replies.afterRead(served)
				}
			} else if replies.other != nil && name == replies.other.Name {
				served = replies.other
			}
			if served == nil {
				writeLogTestStatus(w, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, name))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(served)
		default:
			t.Errorf("unexpected Kubernetes request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(s.Close)
	core, err := typedcorev1.NewForConfig(&rest.Config{Host: s.URL})
	require.NoError(t, err)
	a.k8s.CoreClient = core
	return &calls
}

func writeLogTestStatus(w http.ResponseWriter, err *apierrors.StatusError) {
	status := err.ErrStatus
	status.TypeMeta = metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(int(status.Code))
	_ = json.NewEncoder(w).Encode(status)
}
