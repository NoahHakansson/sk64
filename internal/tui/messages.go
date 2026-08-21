package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/project"
	"github.com/NoahHakansson/sk64/internal/store"
)

type scanDoneMsg struct {
	reqID  int
	result project.ScanResult
	err    error
}

type suggestionCheckedMsg struct {
	reqID, index int
	found        bool
	matched      string
	err          error
}

type suggestionLinkedMsg struct {
	reqID, index int
	err          error
}

type scanLinksAppliedMsg struct{ projectID int64 }

type pendingLink struct {
	workload *store.WorkloadLink
	resource *store.ResourceLink
}

type openProjectPickerMsg struct {
	generation uint64
	link       pendingLink
}

type projectOpenedMsg struct {
	project store.Project
	client  *k8s.Client
	notice  string
}

type projectsLoadedMsg struct {
	reqID    int
	projects []store.Project
	last     string
	err      error
}

type projectProbedMsg struct {
	reqID   int
	project store.Project
	client  *k8s.Client
	err     error
}

type projectIdentityResolvedMsg struct {
	reqID   int
	project store.Project
	target  k8s.ContextInfo
	err     error
}

type projectLinkedMsg struct {
	reqID       int
	projectName string
	err         error
}

type projectLinksMsg struct {
	reqID     int
	workloads []store.WorkloadLink
	resources []store.ResourceLink
	extraNS   []string
	err       error
}

type projectContextMsg struct {
	reqID          int
	found          bool
	kubeServer     string
	serverMismatch bool
	err            error
}

type projectSavedMsg struct{ project store.Project }

type projectUnlinkedMsg struct {
	reqID int
	err   error
}

type namespacesPageMsg struct {
	reqID int
	page  k8s.NamespacePage
	err   error
}

type namespaceFallbackMsg struct {
	reqID int
	err   error
}

type fatalMsg struct{ err error }

func fatalCmd(err error) tea.Cmd {
	return func() tea.Msg { return fatalMsg{err: err} }
}

type resourcesPageMsg struct {
	reqID int
	kind  string
	page  k8s.ResourcePage
	err   error
}

type searchNamespacesMsg struct {
	reqID int
	page  k8s.NamespacePage
	err   error
}

type searchResourcesMsg struct {
	reqID     int
	namespace string
	kind      string
	page      k8s.ResourcePage
	err       error
}

type searchJumpMsg struct {
	generation            uint64
	namespace, kind, name string
}

type deleteDoneMsg struct {
	reqID  int
	result k8s.DeleteResult
}

type outcomeVerb int

const (
	outcomeNone outcomeVerb = iota
	outcomeSaved
	outcomeCreated
	outcomeDeleted
)

type saveNotice int

const (
	saveNoticeComplete saveNotice = iota
	saveNoticeNoEligibleRestart
	saveNoticeRestartSkipped
	saveNoticeConsumerCheckIncomplete
	saveNoticeConsumerCheckUnavailable
	saveNoticeRestartIncomplete
	saveNoticeRestartFailed
)

type resourceOutcome struct {
	verb                  outcomeVerb
	kind, namespace, name string
	save                  saveNotice
}

func (o resourceOutcome) notice() (message, action string) {
	if o.verb == outcomeNone {
		return "", ""
	}
	identity := resourceSubject(o.kind, o.namespace, o.name)
	switch o.verb {
	case outcomeSaved:
		switch o.save {
		case saveNoticeNoEligibleRestart:
			action = "no eligible workloads to restart"
		case saveNoticeRestartSkipped:
			action = "restart skipped"
		case saveNoticeConsumerCheckIncomplete:
			action = "consumer check incomplete"
		case saveNoticeConsumerCheckUnavailable:
			action = "consumer check unavailable"
		case saveNoticeRestartIncomplete:
			action = "workload restart incomplete"
		case saveNoticeRestartFailed:
			action = "workload restart failed"
		}
		return "saved " + identity, action
	case outcomeCreated:
		return "created " + identity, ""
	case outcomeDeleted:
		return "deleted " + identity, ""
	default:
		return "", ""
	}
}

func (o resourceOutcome) render(st *styles, width int) string {
	message, action := o.notice()
	if message == "" {
		return ""
	}
	kind := stateLineSuccess
	switch o.save {
	case saveNoticeRestartIncomplete:
		kind = stateLineIncomplete
	case saveNoticeRestartFailed:
		kind = stateLineError
	}
	return renderStateLine(st, kind, message, action, width)
}

type resourceListChangedMsg struct {
	namespace string
	outcome   resourceOutcome
}

type refsPageMsg struct {
	reqID     int
	source    string
	workloads k8s.WorkloadPage
	pods      k8s.PodPage
	sas       k8s.ServiceAccountPage
	resources k8s.ResourcePage
	err       error
}

type resourceLoadedMsg struct {
	reqID int
	res   k8s.Resource
	err   error
}

type contextsLoadedMsg struct {
	reqID    int
	contexts []k8s.ContextInfo
	err      error
}

type contextProbedMsg struct {
	reqID  int
	name   string
	client *k8s.Client
	err    error
}

type execProbeDoneMsg struct {
	name   string
	client *k8s.Client
	err    error
}

type contextSwitchedMsg struct {
	client *k8s.Client
}

type editorFinishedMsg struct{ err error }

type dryRunDoneMsg struct {
	reqID  int
	result k8s.DryRunResult
}

type saveDoneMsg struct {
	reqID  int
	result k8s.SaveResult
}

type editSavedMsg struct {
	operationID uint64
	outcome     resourceOutcome
	skipRefresh bool
	final       bool
}

type blastRadiusMsg struct {
	reqID int
	index *k8s.RefIndex
	err   error
}

type rolloutDoneMsg struct {
	reqID   int
	results []rolloutResult
}
