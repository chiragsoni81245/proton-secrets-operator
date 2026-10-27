/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	protonv1alpha1 "github.com/chiragsoni81245/proton-secrets-operator/api/v1alpha1"
	"github.com/chiragsoni81245/proton-secrets-operator/internal/metrics"
	"github.com/chiragsoni81245/proton-secrets-operator/internal/proton"
)

const (
	finalizer       = "proton.chiragsoni.org/finalizer"
	fieldOwner      = "proton-secrets-operator"
	minRefresh      = time.Minute
	defaultRefresh  = time.Hour
	terminalBackoff = 10 * time.Minute
)

// ProtonSecretReconciler reconciles a ProtonSecret object
type ProtonSecretReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Proton   proton.Client
	// MaxConcurrent is MaxConcurrentReconciles (default 2).
	MaxConcurrent int
}

// +kubebuilder:rbac:groups=proton.chiragsoni.org,resources=protonsecrets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=proton.chiragsoni.org,resources=protonsecrets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=proton.chiragsoni.org,resources=protonsecrets/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets;daemonsets,verbs=get;patch

// Reconcile makes the target Secret match the references in the ProtonSecret.
func (r *ProtonSecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var ps protonv1alpha1.ProtonSecret
	if err := r.Get(ctx, req.NamespacedName, &ps); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !ps.DeletionTimestamp.IsZero() {
		metrics.LastSync.DeleteLabelValues(ps.Namespace, ps.Name)
		return ctrl.Result{}, r.finalize(ctx, &ps)
	}
	if controllerutil.AddFinalizer(&ps, finalizer) {
		if err := r.Update(ctx, &ps); err != nil {
			return ctrl.Result{}, err
		}
	}

	if ps.Spec.Suspend {
		log.V(1).Info("reconciliation suspended")
		return ctrl.Result{}, nil
	}

	refresh := refreshInterval(&ps)
	start := time.Now()
	res, err := r.sync(ctx, &ps)
	metrics.SyncDuration.Observe(time.Since(start).Seconds())
	metrics.ReconcileTotal.WithLabelValues(metrics.Result(err, errors.Is(err, errForeignSecret))).Inc()
	if err == nil {
		metrics.LastSync.WithLabelValues(ps.Namespace, ps.Name).SetToCurrentTime()
	}
	switch {
	case err == nil:
		return ctrl.Result{RequeueAfter: jitter(refresh)}, nil
	case errors.Is(err, proton.ErrNotFound), errors.Is(err, errForeignSecret):
		// Terminal until the spec or the vault changes; do not hot-loop.
		return ctrl.Result{RequeueAfter: min(refresh, terminalBackoff)}, nil
	default:
		// Transient or auth: rate-limited exponential backoff via the workqueue.
		return res, err
	}
}

var errForeignSecret = errors.New("target Secret exists and is not managed by this ProtonSecret")

func (r *ProtonSecretReconciler) sync(ctx context.Context, ps *protonv1alpha1.ProtonSecret) (ctrl.Result, error) {
	orig := ps.DeepCopy()
	name := targetName(ps)
	ps.Status.ObservedGeneration = ps.Generation
	ps.Status.SecretName = name

	data, err := r.resolve(ctx, ps)
	if err != nil {
		r.fail(ps, "SyncFailed", err)
		_ = r.patchStatus(ctx, ps, orig)
		return ctrl.Result{}, err
	}
	hash := hashData(data)

	changed, err := r.apply(ctx, ps, name, data, hash)
	if err != nil {
		reason := "ApplyFailed"
		if errors.Is(err, errForeignSecret) {
			reason = "SecretConflict"
		}
		r.fail(ps, reason, err)
		_ = r.patchStatus(ctx, ps, orig)
		return ctrl.Result{}, err
	}

	if changed {
		r.Recorder.Eventf(ps, corev1.EventTypeNormal, "Synced", "Secret %s updated", name)
		if err := r.rollout(ctx, ps); err != nil {
			// The Secret is already correct; surface but do not fail the sync.
			r.Recorder.Eventf(ps, corev1.EventTypeWarning, "RolloutFailed", "%v", err)
		}
	}

	now := metav1.Now()
	ps.Status.LastSyncTime = &now
	ps.Status.LastSyncHash = hash
	setCond(ps, protonv1alpha1.ConditionSynced, metav1.ConditionTrue, "Synced", "Proton Pass read succeeded")
	setCond(ps, protonv1alpha1.ConditionReady, metav1.ConditionTrue, "SecretUpToDate", "Secret matches Proton Pass")
	return ctrl.Result{}, r.patchStatus(ctx, ps, orig)
}

// resolve reads every reference once; identical references are fetched once.
func (r *ProtonSecretReconciler) resolve(ctx context.Context, ps *protonv1alpha1.ProtonSecret) (map[string][]byte, error) {
	cache := map[string]string{}
	out := make(map[string][]byte, len(ps.Spec.Data))
	for _, d := range ps.Spec.Data {
		v, ok := cache[d.RemoteRef]
		if !ok {
			var err error
			if v, err = r.Proton.Resolve(ctx, d.RemoteRef); err != nil {
				return nil, fmt.Errorf("key %q: %w", d.SecretKey, err)
			}
			cache[d.RemoteRef] = v
		}
		out[d.SecretKey] = []byte(v)
	}
	return out, nil
}

// apply server-side-applies the Secret and reports whether data changed.
func (r *ProtonSecretReconciler) apply(ctx context.Context, ps *protonv1alpha1.ProtonSecret, name string, data map[string][]byte, hash string) (bool, error) {
	key := types.NamespacedName{Namespace: ps.Namespace, Name: name}
	policy := ps.Spec.Target.CreationPolicy
	if policy == "" {
		policy = protonv1alpha1.CreationPolicyOwner
	}

	var existing corev1.Secret
	err := r.Get(ctx, key, &existing)
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}

	if exists {
		owned := metav1.IsControlledBy(&existing, ps)
		if !owned && policy != protonv1alpha1.CreationPolicyMerge {
			return false, fmt.Errorf("%w: %s", errForeignSecret, name)
		}
		// Compare the live values (not just the annotation) so manual edits are reverted.
		if hashKeys(existing.Data, data) == hash {
			return false, nil
		}
	} else if policy == protonv1alpha1.CreationPolicyNone {
		return false, fmt.Errorf("%w: %s does not exist and creationPolicy is None", errForeignSecret, name)
	}

	labels := map[string]string{protonv1alpha1.ManagedByLabel: protonv1alpha1.ManagedByValue}
	for k, v := range ps.Spec.Target.Labels {
		labels[k] = v
	}
	annotations := map[string]string{protonv1alpha1.HashAnnotation: hash}
	for k, v := range ps.Spec.Target.Annotations {
		annotations[k] = v
	}
	secretType := ps.Spec.Target.Type
	if secretType == "" {
		secretType = corev1.SecretTypeOpaque
	}

	desired := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ps.Namespace, Labels: labels, Annotations: annotations},
		Type:       secretType,
		Data:       data,
	}
	if policy != protonv1alpha1.CreationPolicyMerge {
		if err := controllerutil.SetControllerReference(ps, desired, r.Scheme); err != nil {
			return false, err
		}
	}
	if err := r.Patch(ctx, desired, client.Apply, client.FieldOwner(fieldOwner), client.ForceOwnership); err != nil {
		return false, err
	}
	return true, nil
}

// rollout bumps a pod-template annotation on each target so pods restart.
func (r *ProtonSecretReconciler) rollout(ctx context.Context, ps *protonv1alpha1.ProtonSecret) error {
	stamp := time.Now().UTC().Format(time.RFC3339)
	var errs []error
	for _, t := range ps.Spec.RolloutTargets {
		var obj client.Object
		switch t.Kind {
		case "Deployment":
			obj = &appsv1.Deployment{}
		case "StatefulSet":
			obj = &appsv1.StatefulSet{}
		case "DaemonSet":
			obj = &appsv1.DaemonSet{}
		default:
			continue
		}
		key := types.NamespacedName{Namespace: ps.Namespace, Name: t.Name}
		if err := r.Get(ctx, key, obj); err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", t.Kind, t.Name, err))
			continue
		}
		patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
		switch o := obj.(type) {
		case *appsv1.Deployment:
			setAnno(&o.Spec.Template.ObjectMeta, stamp)
		case *appsv1.StatefulSet:
			setAnno(&o.Spec.Template.ObjectMeta, stamp)
		case *appsv1.DaemonSet:
			setAnno(&o.Spec.Template.ObjectMeta, stamp)
		}
		if err := r.Patch(ctx, obj, patch); err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", t.Kind, t.Name, err))
		}
	}
	return errors.Join(errs...)
}

func setAnno(m *metav1.ObjectMeta, stamp string) {
	if m.Annotations == nil {
		m.Annotations = map[string]string{}
	}
	m.Annotations[protonv1alpha1.RestartedAtAnnotation] = stamp
}

// finalize applies the deletion policy, then releases the ProtonSecret.
func (r *ProtonSecretReconciler) finalize(ctx context.Context, ps *protonv1alpha1.ProtonSecret) error {
	if !controllerutil.ContainsFinalizer(ps, finalizer) {
		return nil
	}
	policy := ps.Spec.Target.DeletionPolicy
	if policy == "" {
		policy = protonv1alpha1.DeletionPolicyDelete
	}
	var s corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Namespace: ps.Namespace, Name: targetName(ps)}, &s)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return err
	case metav1.IsControlledBy(&s, ps):
		if policy == protonv1alpha1.DeletionPolicyRetain {
			// Drop the owner reference or garbage collection would delete it anyway.
			patch := client.MergeFrom(s.DeepCopy())
			s.OwnerReferences = nil
			if err := r.Patch(ctx, &s, patch); err != nil {
				return err
			}
		} else if err := r.Delete(ctx, &s); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	controllerutil.RemoveFinalizer(ps, finalizer)
	return r.Update(ctx, ps)
}

func (r *ProtonSecretReconciler) fail(ps *protonv1alpha1.ProtonSecret, reason string, err error) {
	// Errors from the proton package never contain secret values or tokens.
	setCond(ps, protonv1alpha1.ConditionSynced, metav1.ConditionFalse, reason, err.Error())
	setCond(ps, protonv1alpha1.ConditionReady, metav1.ConditionFalse, reason, err.Error())
	r.Recorder.Eventf(ps, corev1.EventTypeWarning, reason, "%v", err)
}

func (r *ProtonSecretReconciler) patchStatus(ctx context.Context, ps, orig *protonv1alpha1.ProtonSecret) error {
	return r.Status().Patch(ctx, ps, client.MergeFrom(orig))
}

func setCond(ps *protonv1alpha1.ProtonSecret, t string, s metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&ps.Status.Conditions, metav1.Condition{
		Type: t, Status: s, Reason: reason, Message: msg, ObservedGeneration: ps.Generation,
	})
}

func targetName(ps *protonv1alpha1.ProtonSecret) string {
	if ps.Spec.Target.Name != "" {
		return ps.Spec.Target.Name
	}
	return ps.Name
}

func refreshInterval(ps *protonv1alpha1.ProtonSecret) time.Duration {
	d := defaultRefresh
	if ps.Spec.RefreshInterval != nil {
		d = ps.Spec.RefreshInterval.Duration
	}
	return max(d, minRefresh)
}

// jitter spreads refreshes by ±10% so many resources do not hit Proton together.
func jitter(d time.Duration) time.Duration {
	return d + time.Duration((rand.Float64()*0.2-0.1)*float64(d))
}

// hashData is a SHA-256 over sorted key/value pairs.
func hashData(data map[string][]byte) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s=%d:", len(k), k, len(data[k]))
		h.Write(data[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hashKeys hashes the live values restricted to the desired key set, so
// unrelated keys in a Merge-adopted Secret do not cause false drift. A key
// missing from live data yields a hash that cannot match.
func hashKeys(live, desired map[string][]byte) string {
	sub := make(map[string][]byte, len(desired))
	for k := range desired {
		v, ok := live[k]
		if !ok {
			return "missing:" + k
		}
		sub[k] = v
	}
	return hashData(sub)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ProtonSecretReconciler) SetupWithManager(mgr ctrl.Manager) error {
	n := r.MaxConcurrent
	if n <= 0 {
		n = 2
	}
	// Spec changes (generation) and force-sync annotation changes trigger a
	// reconcile; status-only updates do not.
	trigger := predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.AnnotationChangedPredicate{},
	)
	return ctrl.NewControllerManagedBy(mgr).
		For(&protonv1alpha1.ProtonSecret{}, builder.WithPredicates(trigger)).
		Owns(&corev1.Secret{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: n}).
		Named("protonsecret").
		Complete(r)
}
