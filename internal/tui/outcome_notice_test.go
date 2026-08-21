package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/charmbracelet/x/ansi"
)

func TestOutcomeNoticesUseSharedStateLineGrammar(t *testing.T) {
	tests := []struct {
		name    string
		outcome resourceOutcome
		failure bool
		want    string
	}{
		{
			name:    "saved",
			outcome: resourceOutcome{verb: outcomeSaved, kind: k8s.KindSecret, namespace: "default", name: "db-creds"},
			want:    "[success] saved Secret default/db-creds",
		},
		{
			name:    "no eligible workloads",
			outcome: resourceOutcome{verb: outcomeSaved, kind: k8s.KindSecret, namespace: "default", name: "db-creds", save: saveNoticeNoEligibleRestart},
			want:    "[success] saved Secret default/db-creds - no eligible workloads to restart",
		},
		{
			name:    "restart skipped",
			outcome: resourceOutcome{verb: outcomeSaved, kind: k8s.KindSecret, namespace: "default", name: "db-creds", save: saveNoticeRestartSkipped},
			want:    "[success] saved Secret default/db-creds - restart skipped",
		},
		{
			name:    "consumer check incomplete",
			outcome: resourceOutcome{verb: outcomeSaved, kind: k8s.KindSecret, namespace: "default", name: "db-creds", save: saveNoticeConsumerCheckIncomplete},
			want:    "[success] saved Secret default/db-creds - consumer check incomplete",
		},
		{
			name:    "consumer check unavailable",
			outcome: resourceOutcome{verb: outcomeSaved, kind: k8s.KindSecret, namespace: "default", name: "db-creds", save: saveNoticeConsumerCheckUnavailable},
			want:    "[success] saved Secret default/db-creds - consumer check unavailable",
		},
		{
			name:    "workload restart incomplete",
			outcome: resourceOutcome{verb: outcomeSaved, kind: k8s.KindSecret, namespace: "default", name: "db-creds", save: saveNoticeRestartIncomplete},
			want:    "[incomplete] saved Secret default/db-creds - workload restart incomplete",
		},
		{
			name:    "workload restart failed",
			outcome: resourceOutcome{verb: outcomeSaved, kind: k8s.KindSecret, namespace: "default", name: "db-creds", save: saveNoticeRestartFailed},
			want:    "[error] saved Secret default/db-creds - workload restart failed",
		},
		{
			name:    "created",
			outcome: resourceOutcome{verb: outcomeCreated, kind: k8s.KindConfigMap, namespace: "default", name: "app-config"},
			want:    "[success] created ConfigMap default/app-config",
		},
		{
			name:    "deleted",
			outcome: resourceOutcome{verb: outcomeDeleted, kind: k8s.KindSecret, namespace: "default", name: "old-creds"},
			want:    "[success] deleted Secret default/old-creds",
		},
		{
			name:    "failure",
			failure: true,
			want:    "[error] keys unavailable: reload failed - ctrl+r to retry",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			line := test.outcome.render(testStyles(true), 80)
			if test.failure {
				line = renderStateLine(testStyles(true), stateLineError, "keys unavailable: reload failed", "ctrl+r to retry", 80)
			}
			if got := ansi.Strip(line); got != test.want {
				t.Fatalf("notice = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSaveNoticeForResolution(t *testing.T) {
	tests := []struct {
		name       string
		resolution savedResolution
		want       saveNotice
	}{
		{name: "checking", resolution: savedChecking, want: saveNoticeComplete},
		{name: "unavailable", resolution: savedUnavailable, want: saveNoticeConsumerCheckUnavailable},
		{name: "nothing to restart", resolution: savedNothingToRestart, want: saveNoticeNoEligibleRestart},
		{name: "incomplete restart offer", resolution: savedIncompleteRestartOffer, want: saveNoticeConsumerCheckIncomplete},
		{name: "restart offer", resolution: savedRestartOffer, want: saveNoticeComplete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := saveNoticeForResolution(test.resolution); got != test.want {
				t.Fatalf("save notice = %d, want %d", got, test.want)
			}
		})
	}
}

func TestEditSavedBatchOrdersRefreshUnderlyingKeyScreen(t *testing.T) {
	for _, test := range []struct {
		name     string
		reversed bool
	}{
		{name: "message before pop"},
		{name: "pop before message", reversed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			resource := editSecret("10", []byte("old"))
			h := keyHarness(t, resource)
			keyScreen := topKeyScreen(t, h)
			h.send(pushScreenMsg{s: newValueScreen(resource, "DB_PASSWORD", editEnv{}, h.m.(app).styles)})

			outcome := resourceOutcome{
				verb:      outcomeSaved,
				kind:      resource.Kind(),
				namespace: resource.Namespace(),
				name:      resource.Name(),
				save:      saveNoticeNoEligibleRestart,
			}
			saved := func() tea.Msg { return editSavedMsg{operationID: 1, outcome: outcome} }
			commands := []tea.Cmd{saved, popScreen()}
			if test.reversed {
				commands[0], commands[1] = commands[1], commands[0]
			}

			h.drain(tea.Batch(commands...))

			if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top != keyScreen {
				t.Fatalf("top screen = %T, want keyScreen", top)
			}
			if !keyScreen.pending {
				t.Fatal("editSavedMsg did not refresh the underlying key screen")
			}
			if got, want := plainOutcomeNotice(keyScreen.outcome), plainOutcomeNotice(outcome); got != want {
				t.Fatalf("outcome = %q, want %q", got, want)
			}
		})
	}
}

func TestResolvedSaveOutcomeRejectsLateProvisionalOutcome(t *testing.T) {
	resource := editSecret("10", []byte("old"))
	h := keyHarness(t, resource)
	keyScreen := topKeyScreen(t, h)
	resolved := resourceOutcome{
		verb:      outcomeSaved,
		kind:      resource.Kind(),
		namespace: resource.Namespace(),
		name:      resource.Name(),
		save:      saveNoticeNoEligibleRestart,
	}
	provisional := resolved
	provisional.save = saveNoticeComplete

	h.send(editSavedMsg{operationID: 1, outcome: resolved, skipRefresh: true, final: true})
	reqID := keyScreen.reqID
	h.send(resourceLoadedMsg{reqID: reqID, res: resource})
	h.send(editSavedMsg{operationID: 1, outcome: provisional})

	if keyScreen.outcome != resolved || !keyScreen.outcomeFinal {
		t.Fatalf("late provisional outcome replaced final outcome: %+v final %t", keyScreen.outcome, keyScreen.outcomeFinal)
	}
	if keyScreen.reqID != reqID || keyScreen.pending {
		t.Fatalf("late provisional restarted the operation reload: reqID %d, want %d, pending %t", keyScreen.reqID, reqID, keyScreen.pending)
	}
}

func TestKeyScreenSaveNoticeOperationEpoch(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "older final cannot replace newer restart skipped notice",
			run: func(t *testing.T) {
				resource := editSecret("10", []byte("old"))
				h := keyHarness(t, resource)
				screen := topKeyScreen(t, h)
				newer := resourceOutcome{
					verb:      outcomeSaved,
					kind:      resource.Kind(),
					namespace: resource.Namespace(),
					name:      resource.Name(),
					save:      saveNoticeRestartSkipped,
				}
				older := newer
				older.save = saveNoticeNoEligibleRestart

				h.send(editSavedMsg{operationID: 2, outcome: newer, skipRefresh: true, final: true})
				refreshReqID := screen.reqID
				h.send(editSavedMsg{operationID: 1, outcome: older, skipRefresh: true, final: true})

				if screen.outcome != newer || screen.outcomeOperationID != 2 || !screen.outcomeFinal {
					t.Fatalf("stale final replaced newer outcome: %+v operation %d final %t", screen.outcome, screen.outcomeOperationID, screen.outcomeFinal)
				}
				if screen.reqID != refreshReqID {
					t.Fatalf("stale final changed reload request from %d to %d", refreshReqID, screen.reqID)
				}
			},
		},
		{
			name: "newer save supersedes pending reload",
			run: func(t *testing.T) {
				resource := editSecret("10", []byte("old"))
				h := keyHarness(t, resource)
				screen := topKeyScreen(t, h)
				newSavedMessage := func(flow *editFlow) editSavedMsg {
					t.Helper()
					flow.phase = phaseSaving
					_, reqID := flow.start(flow.ctx)
					_, cmd := flow.Update(saveDoneMsg{reqID: reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
					commandMessage := cmd()
					batch, ok := commandMessage.(tea.BatchMsg)
					if !ok || len(batch) == 0 {
						t.Fatalf("save command = %T, want non-empty tea.BatchMsg", commandMessage)
					}
					savedMessage := batch[0]()
					message, ok := savedMessage.(editSavedMsg)
					if !ok {
						t.Fatalf("first save command = %T, want editSavedMsg", savedMessage)
					}
					return message
				}

				firstFlow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource.Clone(), "DB_PASSWORD", []byte("first"), h.m.(app).styles)
				secondFlow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource.Clone(), "DB_PASSWORD", []byte("second"), h.m.(app).styles)
				first := newSavedMessage(firstFlow)
				second := newSavedMessage(secondFlow)
				first.outcome.save = saveNoticeNoEligibleRestart
				second.outcome.save = saveNoticeRestartSkipped
				if first.operationID == 0 || second.operationID <= first.operationID {
					t.Fatalf("save operation ids = %d then %d, want increasing non-zero ids", first.operationID, second.operationID)
				}

				_, firstReload := screen.Update(first)
				firstReloadReqID := screen.reqID
				_, secondReload := screen.Update(second)
				secondReloadReqID := screen.reqID

				if firstReload == nil || secondReload == nil {
					t.Fatalf("reload commands = first %t second %t, want both", firstReload != nil, secondReload != nil)
				}
				if secondReloadReqID == firstReloadReqID || !screen.pending {
					t.Fatalf("newer save reused pending reload %d; current %d pending %t", firstReloadReqID, secondReloadReqID, screen.pending)
				}
				if screen.outcome != second.outcome || screen.outcomeOperationID != second.operationID {
					t.Fatalf("newer save outcome = %+v operation %d, want %+v operation %d", screen.outcome, screen.outcomeOperationID, second.outcome, second.operationID)
				}

				screen.Update(resourceLoadedMsg{reqID: firstReloadReqID, res: resource})
				if screen.reqID != secondReloadReqID || !screen.pending {
					t.Fatalf("superseded reload completed newer request: reqID %d pending %t", screen.reqID, screen.pending)
				}
			},
		},
		{
			name: "cleared operation ignores late final",
			run: func(t *testing.T) {
				resource := editSecret("10", []byte("old"))
				h := keyHarness(t, resource)
				screen := topKeyScreen(t, h)
				provisional := resourceOutcome{
					verb:      outcomeSaved,
					kind:      resource.Kind(),
					namespace: resource.Namespace(),
					name:      resource.Name(),
				}
				final := provisional
				final.save = saveNoticeNoEligibleRestart

				h.send(editSavedMsg{operationID: 7, outcome: provisional})
				h.keys("?")
				refreshReqID := screen.reqID
				h.send(editSavedMsg{operationID: 7, outcome: final, skipRefresh: true, final: true})

				if screen.outcome.verb != outcomeNone || !screen.outcomeCleared {
					t.Fatalf("late final repainted cleared outcome: %+v cleared %t", screen.outcome, screen.outcomeCleared)
				}
				if screen.reqID != refreshReqID {
					t.Fatalf("late final changed cleared operation reload from %d to %d", refreshReqID, screen.reqID)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func TestNoRestartSaveNoticeSurvivesKeyReload(t *testing.T) {
	secretValue := "new-secret-value-that-must-not-enter-the-notice"
	for _, test := range []struct {
		name          string
		wholeResource bool
	}{
		{name: "single key"},
		{name: "whole resource", wholeResource: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, keyScreen := noRestartSaveHarness(t, test.wholeResource, secretValue)
			want := "[success] saved Secret default/db-creds - no eligible workloads to restart"
			immediate := h.view()
			if !keyScreen.pending || !strings.Contains(immediate, want) || !strings.Contains(immediate, "[loading] loading keys") {
				t.Fatalf("immediate post-save state = pending %t view:\n%s", keyScreen.pending, immediate)
			}
			if notice := plainOutcomeNotice(keyScreen.outcome); strings.Contains(notice, secretValue) {
				t.Fatalf("save outcome contains edited secret value: %q", notice)
			}

			reloaded := editSecret("11", []byte(secretValue))
			h.send(resourceLoadedMsg{reqID: keyScreen.reqID, res: reloaded})
			if keyScreen.pending || !strings.Contains(h.view(), want) {
				t.Fatalf("post-reload state = pending %t view:\n%s", keyScreen.pending, h.view())
			}

			h.keys("?")
			if keyScreen.outcome.verb != outcomeNone {
				t.Fatalf("global deliberate key left save outcome %q", plainOutcomeNotice(keyScreen.outcome))
			}
			h.keys("esc")
			if strings.Contains(h.view(), want) {
				t.Fatalf("cleared save outcome returned after closing help: %q", h.view())
			}
		})
	}
}

func TestPendingConsumerCheckUpdatesSavedNoticeWithoutSecondRefresh(t *testing.T) {
	incomplete := k8s.NewRefIndex()
	incomplete.AddWorkload(k8s.Workload{Kind: k8s.KindDeployment, Name: "web", Namespace: "default", Spec: podSpecWithRef("db-creds", k8s.TagEnv)})
	incomplete.AddSourceError("pods")
	tests := []struct {
		name   string
		result blastRadiusMsg
		want   saveNotice
		offer  bool
	}{
		{name: "no eligible restart", result: blastRadiusMsg{index: k8s.NewRefIndex()}, want: saveNoticeNoEligibleRestart},
		{name: "incomplete restart offer", result: blastRadiusMsg{index: incomplete}, want: saveNoticeConsumerCheckIncomplete, offer: true},
		{name: "consumer check unavailable", result: blastRadiusMsg{err: errors.New("consumer scan failed")}, want: saveNoticeConsumerCheckUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resource := editSecret("10", []byte("old"))
			h := keyHarness(t, resource)
			keyScreen := topKeyScreen(t, h)
			flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource, "DB_PASSWORD", []byte("new"), h.m.(app).styles)
			h.send(pushScreenMsg{s: flow})
			enterSaving(h)
			h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
			radiusReqID := flow.radiusLoader.reqID

			if flow.savedResolution() != savedChecking || keyScreen.outcome.save != saveNoticeComplete {
				t.Fatalf("initial saved state = resolution %d notice %d", flow.savedResolution(), keyScreen.outcome.save)
			}
			refreshReqID := keyScreen.reqID
			result := test.result
			result.reqID = radiusReqID
			h.send(result)

			if keyScreen.reqID != refreshReqID {
				t.Fatalf("notice update started second refresh %d, want %d", keyScreen.reqID, refreshReqID)
			}
			if keyScreen.outcome.save != test.want {
				t.Fatalf("resolved notice = %d, want %d", keyScreen.outcome.save, test.want)
			}
			if test.offer {
				if flow.savedResolution() != savedIncompleteRestartOffer {
					t.Fatalf("resolved offer = %d, want %d", flow.savedResolution(), savedIncompleteRestartOffer)
				}
				return
			}
			h.send(resourceLoadedMsg{reqID: keyScreen.reqID, res: flow.res})
			h.keys("enter")
			if !strings.Contains(h.view(), plainOutcomeNotice(keyScreen.outcome)) {
				t.Fatalf("resolved notice did not survive reload and dismissal:\n%s", h.view())
			}
		})
	}
}

func TestSaveNoticeDistinguishesRestartResolution(t *testing.T) {
	t.Run("restart skipped", func(t *testing.T) {
		h, flow, keyScreen := rolloutOfferHarness(t)
		h.keys("esc")

		want := "[success] saved Secret default/db-creds - restart skipped"
		if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top != keyScreen {
			t.Fatalf("top screen = %T, want keyScreen", top)
		}
		if !strings.Contains(h.view(), want) {
			t.Fatalf("restart-skipped outcome missing:\n%s", h.view())
		}
		h.send(resourceLoadedMsg{reqID: keyScreen.reqID, res: flow.res})
		if !strings.Contains(h.view(), want) {
			t.Fatalf("restart-skipped outcome did not survive reload:\n%s", h.view())
		}
	})

	t.Run("consumer check unavailable with reload failure and no color", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		resource := editSecret("10", []byte("old"))
		h := keyHarness(t, resource)
		keyScreen := topKeyScreen(t, h)
		flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource, "DB_PASSWORD", []byte("new"), h.m.(app).styles)
		h.send(pushScreenMsg{s: flow})
		enterSaving(h)
		h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
		h.send(blastRadiusMsg{reqID: flow.radiusLoader.reqID, err: errors.New("consumer scan failed")})

		if flow.phase != phaseSaved || !keyScreen.pending {
			t.Fatalf("saved unavailable state = phase %d key pending %t", flow.phase, keyScreen.pending)
		}
		reloadErr := errors.New("reload failed after save")
		h.send(resourceLoadedMsg{reqID: keyScreen.reqID, err: reloadErr})
		h.keys("enter")

		view := h.view()
		for _, want := range []string{
			"[success] saved Secret default/db-creds - consumer check unavailable",
			"[error] keys unavailable: reload failed after save - ctrl+r to retry",
		} {
			if !strings.Contains(view, want) {
				t.Fatalf("save success plus reload failure lost %q:\n%s", want, view)
			}
		}
	})
}

func TestCreateDeleteNoticesSurviveFailedListReload(t *testing.T) {
	tests := []struct {
		name    string
		succeed func(*testing.T) (*harness, *resourceScreen)
		want    string
	}{
		{
			name: "create",
			succeed: func(t *testing.T) (*harness, *resourceScreen) {
				flow, h := createFlowHarness(t, k8s.KindSecret, "created-secret", 0)
				writeFlowFile(t, flow, "password: create-secret-value\n")
				h.send(editorFinishedMsg{})
				h.keys("Y")
				h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
				passCommitGate(h)
				h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
				return h, h.m.(app).stack[len(h.m.(app).stack)-1].(*resourceScreen)
			},
			want: "[success] created Secret default/created-secret",
		},
		{
			name: "delete",
			succeed: func(t *testing.T) (*harness, *resourceScreen) {
				h, resource := deleteHarness(t, false)
				prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*deleteConfirm)
				h.send(blastRadiusMsg{reqID: prompt.radiusLoader.reqID, index: k8s.NewRefIndex()})
				prompt.input.SetValue(resource.Name())
				h.keys("enter")
				return h, h.m.(app).stack[len(h.m.(app).stack)-1].(*resourceScreen)
			},
			want: "[success] deleted Secret default/app-credentials",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, screen := test.succeed(t)
			if !screen.pending || !strings.Contains(h.view(), test.want) {
				t.Fatalf("immediate %s outcome = pending %t view:\n%s", test.name, screen.pending, h.view())
			}

			reloadErr := errors.New("resource list reload failed")
			h.send(
				resourcesPageMsg{reqID: screen.reqID, kind: k8s.KindSecret, err: reloadErr},
				resourcesPageMsg{reqID: screen.reqID, kind: k8s.KindConfigMap, err: reloadErr},
			)
			view := h.view()
			for _, want := range []string{test.want, "[error] resources unavailable: resource list reload failed"} {
				if !strings.Contains(view, want) {
					t.Fatalf("%s success plus reload failure lost %q:\n%s", test.name, want, view)
				}
			}
		})
	}
}

func noRestartSaveHarness(t *testing.T, wholeResource bool, secretValue string) (*harness, *keyScreen) {
	t.Helper()
	resource := editSecret("10", []byte("old"))
	h := keyHarness(t, resource)
	keyScreen := topKeyScreen(t, h)
	var flow *editFlow
	if wholeResource {
		flow = newResourceRevertFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource, map[string]string{"DB_PASSWORD": secretValue}, nil, h.m.(app).styles)
	} else {
		flow = newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource, "DB_PASSWORD", []byte(secretValue), h.m.(app).styles)
	}
	h.send(pushScreenMsg{s: flow})
	resolveNoConsumers(h, flow)
	enterSaving(h)
	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
	resolveNoConsumers(h, flow)
	h.keys("enter")
	return h, keyScreen
}

func plainOutcomeNotice(outcome resourceOutcome) string {
	return ansi.Strip(outcome.render(testStyles(true), 0))
}
