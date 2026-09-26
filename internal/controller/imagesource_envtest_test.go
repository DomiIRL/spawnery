/*
Copyright paul_wtf.

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
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	spawneryv1alpha1 "github.com/spawnery/spawnery/api/v1alpha1"
	"github.com/spawnery/spawnery/internal/testenv"
)

// The reserved-prefix rule is a CEL expression on the CRD, so the only thing
// that can prove it works is an API server holding the generated CRD and
// refusing the object. internal/podspec/env_test.go checks that the rule still
// spells the prefix the Go constant does; this checks that the rule the API
// server compiles actually denies anything.
//
// Both halves are needed. A rule with a typo in the CEL -- an unbalanced
// paren, `startWith` for `startsWith` -- fails to compile, and a CRD whose
// validation does not compile is accepted with the rule inert on some
// versions. Then every group is admitted, a group shadowing
// SPAWNERY_OPERATOR_ENDPOINT points its agents at an address of its choosing,
// and the marker test goes on passing because the literal in the rule is
// still right.
func imageSourceGroup(ns string, mutate func(*spawneryv1alpha1.ServerGroup)) *spawneryv1alpha1.ServerGroup {
	g := &spawneryv1alpha1.ServerGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "lobby", Namespace: ns},
		Spec: spawneryv1alpha1.ServerGroupSpec{
			NetworkRef: spawneryv1alpha1.ObjectRef{Name: "production"},
			Type:       spawneryv1alpha1.ServerGroupEphemeral,
			Image:      "ghcr.io/spawnery/paper:1.21.4-0.1.0",
			MaxPlayers: 100,
			Scaling:    &spawneryv1alpha1.ScalingSpec{MinReplicas: 1, MaxReplicas: 2, SpareSlots: 10},
		},
	}
	mutate(g)
	return g
}

func TestTheAPIServerTakesExactlyOnePluginSource(t *testing.T) {
	c, ctx := testenv.Client(t)
	ns := testenv.Namespace(t, ctx, c)

	for _, bad := range []struct {
		why string
		ep  spawneryv1alpha1.ExtraPlugins
	}{
		{"both sources", spawneryv1alpha1.ExtraPlugins{ClaimName: "plugins", Image: "registry.example.net/lobby-plugins:1"}},
		{"neither source", spawneryv1alpha1.ExtraPlugins{}},
		{"a pull policy without an image", spawneryv1alpha1.ExtraPlugins{ClaimName: "plugins", PullPolicy: corev1.PullAlways}},
	} {
		ep := bad.ep
		if err := c.Create(ctx, imageSourceGroup(ns, func(g *spawneryv1alpha1.ServerGroup) { g.Spec.ExtraPlugins = &ep })); err == nil {
			t.Errorf("%s was admitted", bad.why)
		}
	}
	ok := imageSourceGroup(ns, func(g *spawneryv1alpha1.ServerGroup) {
		g.Spec.ExtraPlugins = &spawneryv1alpha1.ExtraPlugins{Image: "registry.example.net/lobby-plugins@sha256:" + strings.Repeat("a", 64)}
		g.Spec.ExtraFiles = &spawneryv1alpha1.ExtraFiles{Image: "registry.example.net/lobby-files:1", PullPolicy: corev1.PullAlways}
		g.Spec.Substitution = &spawneryv1alpha1.Substitution{Prefix: "SECRET_"}
	})
	if err := c.Create(ctx, ok); err != nil {
		t.Fatalf("image sources with a substitution were refused: %v", err)
	}
}

func TestTheAPIServerRefusesABadSubstitutionPrefix(t *testing.T) {
	c, ctx := testenv.Client(t)
	ns := testenv.Namespace(t, ctx, c)
	for i, prefix := range []string{"", "secret_", "1X", "SECRET-"} {
		p, name := prefix, fmt.Sprintf("lobby-%d", i)
		if err := c.Create(ctx, imageSourceGroup(ns, func(g *spawneryv1alpha1.ServerGroup) {
			g.Name = name
			g.Spec.Substitution = &spawneryv1alpha1.Substitution{Prefix: p}
		})); err == nil {
			t.Errorf("prefix %q was admitted", p)
		}
	}
}
