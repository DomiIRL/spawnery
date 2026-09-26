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

package agentserver

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"

	"github.com/spawnery/spawnery/internal/agent"
	"github.com/spawnery/spawnery/internal/agentpb"
	"github.com/spawnery/spawnery/internal/grpcauth"
	"github.com/spawnery/spawnery/internal/netstate"
	"github.com/spawnery/spawnery/internal/netstatus"
)

type fakeStatus struct {
	gotNamespace string
	gotAudience  netstate.Audience
	gotTarget    string
	res          *agentpb.StatusResult
	err          error
}

func (f *fakeStatus) Status(_ context.Context, namespace string, audience netstate.Audience, target string) (*agentpb.StatusResult, error) {
	f.gotNamespace, f.gotAudience, f.gotTarget = namespace, audience, target
	return f.res, f.err
}

func TestAnswerStatusIsNamespaceAndAudienceBound(t *testing.T) {
	f := &fakeStatus{res: &agentpb.StatusResult{Servers: 3}}
	s := &Server{opts: Options{Status: f}}
	id := grpcauth.Identity{Namespace: "games", Role: agent.RoleServer}
	resp := s.answerStatus(context.Background(), logr.Discard(), id, 7, &agentpb.StatusRequest{Target: "lobby"})
	if resp.GetId() != 7 || resp.GetStatus().GetServers() != 3 {
		t.Fatalf("answer = %+v", resp)
	}
	if f.gotNamespace != "games" || f.gotAudience != netstate.ForServers || f.gotTarget != "lobby" {
		t.Errorf("asked %q/%v/%q, want games/ForServers/lobby", f.gotNamespace, f.gotAudience, f.gotTarget)
	}
}

func TestAnswerStatusRefusals(t *testing.T) {
	for _, c := range []struct {
		err    error
		reason agentpb.RequestError_Reason
		says   string
	}{
		{netstatus.ErrUnknownTarget, agentpb.RequestError_NOT_FOUND, "no group, server or proxy by that name is on this network"},
		{errors.New("cache not synced"), agentpb.RequestError_UNAVAILABLE, "the operator could not read the network just now"},
	} {
		s := &Server{opts: Options{Status: &fakeStatus{err: c.err}}}
		resp := s.answerStatus(context.Background(), logr.Discard(), grpcauth.Identity{Namespace: "games"}, 1, &agentpb.StatusRequest{})
		if resp.GetError().GetReason() != c.reason || resp.GetError().GetMessage() != c.says {
			t.Errorf("%v -> %+v, want %v %q", c.err, resp.GetError(), c.reason, c.says)
		}
	}
	s := &Server{opts: Options{}}
	if resp := s.answerStatus(context.Background(), logr.Discard(), grpcauth.Identity{}, 1, &agentpb.StatusRequest{}); resp.GetError().GetReason() != agentpb.RequestError_UNAVAILABLE {
		t.Errorf("no status source -> %+v, want UNAVAILABLE", resp)
	}
}
