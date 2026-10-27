package controller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	protonv1alpha1 "github.com/chiragsoni81245/proton-secrets-operator/api/v1alpha1"
	"github.com/chiragsoni81245/proton-secrets-operator/internal/proton"
)

const (
	testName  = "grafana"
	testNS    = "monitoring"
	refUser   = "pass://homelab/grafana/username"
	refPass   = "pass://homelab/grafana/password"
	userValue = "admin"
	passValue = "s3cret"
)

func newReconciler(t *testing.T, fp *proton.Fake, objs ...client.Object) (*ProtonSecretReconciler, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = protonv1alpha1.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&protonv1alpha1.ProtonSecret{}).WithObjects(objs...).Build()
	return &ProtonSecretReconciler{Client: c, Scheme: s, Recorder: events.NewFakeRecorder(50), Proton: fp}, c
}

func sample() *protonv1alpha1.ProtonSecret {
	return &protonv1alpha1.ProtonSecret{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNS, UID: "uid-1", Generation: 1},
		Spec: protonv1alpha1.ProtonSecretSpec{Data: []protonv1alpha1.DataSource{
			{SecretKey: "admin-user", RemoteRef: refUser},
			{SecretKey: "admin-password", RemoteRef: refPass},
		}},
	}
}

func reconcile(t *testing.T, r *ProtonSecretReconciler) (ctrl.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNS, Name: testName}})
}

func getSecret(t *testing.T, c client.Client) *corev1.Secret {
	t.Helper()
	var s corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: testName}, &s); err != nil {
		t.Fatalf("get secret: %v", err)
	}
	return &s
}

func TestCreateUpdateAndDriftRevert(t *testing.T) {
	fp := proton.NewFake(map[string]string{
		refUser: userValue,
		refPass: passValue,
	})
	r, c := newReconciler(t, fp, sample())

	// Reconcile #1 adds the finalizer and creates the Secret.
	if res, err := reconcile(t, r); err != nil || res.RequeueAfter == 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	s := getSecret(t, c)
	if string(s.Data["admin-password"]) != passValue || s.Labels[protonv1alpha1.ManagedByLabel] == "" {
		t.Fatalf("unexpected secret: %+v", s)
	}
	if !metav1.IsControlledBy(s, sample()) {
		t.Fatalf("secret not owned by ProtonSecret: %+v", s.OwnerReferences)
	}

	// Proton value changes -> Secret follows on next reconcile.
	fp.Set(refPass, "rotated")
	if _, err := reconcile(t, r); err != nil {
		t.Fatal(err)
	}
	if got := string(getSecret(t, c).Data["admin-password"]); got != "rotated" {
		t.Fatalf("not updated, got %q", got)
	}

	// Manual tampering is reverted.
	s = getSecret(t, c)
	s.Data["admin-password"] = []byte("hacked")
	if err := c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcile(t, r); err != nil {
		t.Fatal(err)
	}
	if got := string(getSecret(t, c).Data["admin-password"]); got != "rotated" {
		t.Fatalf("drift not reverted, got %q", got)
	}

	var ps protonv1alpha1.ProtonSecret
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: testName}, &ps)
	if ps.Status.LastSyncHash == "" || ps.Status.SecretName != "grafana" {
		t.Fatalf("status not set: %+v", ps.Status)
	}
}

func TestProtonDownLeavesSecretUntouched(t *testing.T) {
	fp := proton.NewFake(map[string]string{
		refUser: userValue,
		refPass: passValue,
	})
	r, c := newReconciler(t, fp, sample())
	if _, err := reconcile(t, r); err != nil {
		t.Fatal(err)
	}

	fp.Err = proton.ErrTransient
	if _, err := reconcile(t, r); !errors.Is(err, proton.ErrTransient) {
		t.Fatalf("want transient error for backoff, got %v", err)
	}
	if got := string(getSecret(t, c).Data["admin-password"]); got != passValue {
		t.Fatalf("secret was modified while Proton was down: %q", got)
	}
}

func TestNotFoundIsTerminalNoHotLoop(t *testing.T) {
	r, _ := newReconciler(t, proton.NewFake(nil), sample())
	res, err := reconcile(t, r)
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("want delayed requeue and nil error, got %+v %v", res, err)
	}
}

func TestRefusesToAdoptForeignSecret(t *testing.T) {
	foreign := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNS},
		Data:       map[string][]byte{"admin-user": []byte("x")},
	}
	fp := proton.NewFake(map[string]string{
		refUser: userValue,
		refPass: passValue,
	})
	r, c := newReconciler(t, fp, sample(), foreign)
	if _, err := reconcile(t, r); err != nil {
		t.Fatalf("conflict should be terminal (nil err), got %v", err)
	}
	if got := string(getSecret(t, c).Data["admin-user"]); got != "x" {
		t.Fatalf("foreign secret overwritten: %q", got)
	}
}

func TestDeleteRemovesOwnedSecret(t *testing.T) {
	fp := proton.NewFake(map[string]string{
		refUser: userValue,
		refPass: passValue,
	})
	r, c := newReconciler(t, fp, sample())
	if _, err := reconcile(t, r); err != nil {
		t.Fatal(err)
	}
	var ps protonv1alpha1.ProtonSecret
	key := types.NamespacedName{Namespace: testNS, Name: testName}
	_ = c.Get(context.Background(), key, &ps)
	if err := c.Delete(context.Background(), &ps); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcile(t, r); err != nil {
		t.Fatal(err)
	}
	var s corev1.Secret
	if err := c.Get(context.Background(), key, &s); err == nil {
		t.Fatal("secret should be deleted with DeletionPolicy=Delete")
	}
}

func TestHashStable(t *testing.T) {
	a := hashData(map[string][]byte{"a": []byte("1"), "b": []byte("2")})
	b := hashData(map[string][]byte{"b": []byte("2"), "a": []byte("1")})
	if a != b {
		t.Fatal("hash must be order independent")
	}
	if a == hashData(map[string][]byte{"a": []byte("12"), "b": []byte("")}) {
		t.Fatal("hash must be unambiguous across key/value boundaries")
	}
}
