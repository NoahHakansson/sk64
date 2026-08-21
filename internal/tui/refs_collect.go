package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
)

type refsCollector struct {
	loader
	ctx        context.Context
	client     *k8s.Client
	namespace  string
	existence  bool
	index      *k8s.RefIndex
	pendingSrc int
	complete   bool
	cancelled  bool
	loadCtx    context.Context
}

func newRefsCollector(ctx context.Context, client *k8s.Client, namespace string, existence bool) refsCollector {
	return refsCollector{ctx: ctx, client: client, namespace: namespace, existence: existence}
}

func (c *refsCollector) startCollect() tea.Cmd {
	ctx, reqID := c.start(c.ctx)
	c.loadCtx = ctx
	c.index = k8s.NewRefIndex()
	c.complete = false
	c.cancelled = false
	c.pendingSrc = len(k8s.WorkloadKinds) + 2
	if c.existence {
		c.pendingSrc += 2
	}

	cmds := make([]tea.Cmd, 0, c.pendingSrc)
	for _, kind := range k8s.WorkloadKinds {
		cmds = append(cmds, c.fetchWorkloads(ctx, reqID, kind, ""))
	}
	cmds = append(cmds, c.fetchPods(ctx, reqID, ""), c.fetchServiceAccounts(ctx, reqID, ""))
	if c.existence {
		cmds = append(cmds, c.fetchResources(ctx, reqID, k8s.KindSecret, ""), c.fetchResources(ctx, reqID, k8s.KindConfigMap, ""))
	}
	return tea.Batch(cmds...)
}

func (c *refsCollector) fetchWorkloads(ctx context.Context, reqID int, kind, continueToken string) tea.Cmd {
	return func() tea.Msg {
		page, err := c.client.ListWorkloads(ctx, c.namespace, kind, k8s.DefaultPageSize, continueToken)
		return refsPageMsg{reqID: reqID, source: k8s.SourceName(kind), workloads: page, err: err}
	}
}

func (c *refsCollector) fetchPods(ctx context.Context, reqID int, continueToken string) tea.Cmd {
	return func() tea.Msg {
		page, err := c.client.ListPods(ctx, c.namespace, k8s.DefaultPageSize, continueToken)
		return refsPageMsg{reqID: reqID, source: "pods", pods: page, err: err}
	}
}

func (c *refsCollector) fetchServiceAccounts(ctx context.Context, reqID int, continueToken string) tea.Cmd {
	return func() tea.Msg {
		page, err := c.client.ListServiceAccounts(ctx, c.namespace, k8s.DefaultPageSize, continueToken)
		return refsPageMsg{reqID: reqID, source: "serviceaccounts", sas: page, err: err}
	}
}

func (c *refsCollector) fetchResources(ctx context.Context, reqID int, kind, continueToken string) tea.Cmd {
	return func() tea.Msg {
		var page k8s.ResourcePage
		var err error
		if kind == k8s.KindSecret {
			page, err = c.client.ListSecrets(ctx, c.namespace, k8s.DefaultPageSize, continueToken)
		} else {
			page, err = c.client.ListConfigMaps(ctx, c.namespace, k8s.DefaultPageSize, continueToken)
		}
		return refsPageMsg{reqID: reqID, source: resourceSource(kind), resources: page, err: err}
	}
}

func (c *refsCollector) handleRefsPage(msg refsPageMsg) (tea.Cmd, bool) {
	if msg.reqID != c.reqID || !c.pending {
		return nil, false
	}
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			c.finish(msg.reqID)
			c.cancelled = true
			return nil, true
		}
		c.index.AddSourceError(msg.source)
		return nil, c.finishSource(msg.reqID)
	}

	continueToken := ""
	var next tea.Cmd
	switch msg.source {
	case "pods":
		for _, pod := range msg.pods.Items {
			c.index.AddPod(pod)
		}
		continueToken = msg.pods.Continue
		if continueToken != "" {
			next = c.fetchPods(c.loadCtx, msg.reqID, continueToken)
		}
	case "serviceaccounts":
		for _, serviceAccount := range msg.sas.Items {
			c.index.AddServiceAccount(serviceAccount)
		}
		continueToken = msg.sas.Continue
		if continueToken != "" {
			next = c.fetchServiceAccounts(c.loadCtx, msg.reqID, continueToken)
		}
	case "secrets", "configmaps":
		names := make([]string, 0, len(msg.resources.Items))
		kind := k8s.KindSecret
		if msg.source == "configmaps" {
			kind = k8s.KindConfigMap
		}
		for _, resource := range msg.resources.Items {
			names = append(names, resource.Name())
		}
		continueToken = msg.resources.Continue
		c.index.AddExisting(kind, names, continueToken == "")
		if continueToken != "" {
			next = c.fetchResources(c.loadCtx, msg.reqID, kind, continueToken)
		}
	default:
		kind := workloadKindForSource(msg.source)
		for _, workload := range msg.workloads.Items {
			c.index.AddWorkload(workload)
		}
		continueToken = msg.workloads.Continue
		if continueToken != "" {
			next = c.fetchWorkloads(c.loadCtx, msg.reqID, kind, continueToken)
		}
	}
	if continueToken != "" {
		return next, false
	}
	return nil, c.finishSource(msg.reqID)
}

func (c *refsCollector) finishSource(reqID int) bool {
	c.pendingSrc--
	if c.pendingSrc == 0 {
		c.finish(reqID)
		c.complete = true
		return true
	}
	return false
}

func workloadKindForSource(source string) string {
	for _, kind := range k8s.WorkloadKinds {
		if k8s.SourceName(kind) == source {
			return kind
		}
	}
	return ""
}

func resourceSource(kind string) string {
	if kind == k8s.KindSecret {
		return "secrets"
	}
	return "configmaps"
}
