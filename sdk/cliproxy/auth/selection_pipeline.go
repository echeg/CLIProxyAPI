package auth

import (
	"context"
	"slices"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// PipelineSelector applies the explicit selection of the request's routing profile:
// the profile's preferred credential first, then the primary slot, then the tiebreak
// when the primary narrows, and round-robin for any ties left over. Narrowing slots
// keep the best group of candidates; picker slots choose one credential by delegating
// to the legacy selectors in a picker-only context. The slots are read at Pick time,
// so one selector serves every client key and a selection change needs no rebuild.
type PipelineSelector struct {
	sharedSelection func() config.SelectionConfig
	roundRobin      RoundRobinSelector
	weighted        WeightedRoundRobinSelector
	fillFirst       FillFirstSelector
	nowFunc         func() time.Time
}

// NewPipelineSelector returns a pipeline selector. shared supplies the shared
// selection for requests whose context carries no routing profile; it may be nil,
// in which case the default selection applies.
func NewPipelineSelector(shared func() config.SelectionConfig) *PipelineSelector {
	return &PipelineSelector{sharedSelection: shared}
}

// pipelineCandidatesKey marks candidates that the caller already reduced to the
// pipeline candidate set (availableForPipeline semantics), so Pick does not refilter.
type pipelineCandidatesKey struct{}

func withPipelineCandidates(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, pipelineCandidatesKey{}, true)
}

func pipelineCandidatesPrefiltered(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	prefiltered, _ := ctx.Value(pipelineCandidatesKey{}).(bool)
	return prefiltered
}

func (s *PipelineSelector) now() time.Time {
	if s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

// selection returns the slots for the request: the context profile's selection,
// otherwise the shared selection, otherwise the default.
func (s *PipelineSelector) selection(ctx context.Context) config.SelectionConfig {
	if profile := routingProfileFromContext(ctx); profile != nil && profile.selection.Primary != "" {
		return profile.selection
	}
	if s.sharedSelection != nil {
		if shared := s.sharedSelection(); shared.Primary != "" {
			return shared
		}
	}
	return config.DefaultSelection()
}

// activeSlots returns the slots that run for selection: a picker primary stops there.
func activeSlots(selection config.SelectionConfig) []string {
	if config.IsPickerSlot(selection.Primary) || selection.Tiebreak == "" {
		return []string{selection.Primary}
	}
	return []string{selection.Primary, selection.Tiebreak}
}

// weightedCandidates drops zero-weight credentials when weighted-round-robin is an
// active slot for the request, except the preferred credential, which Pick takes
// regardless of weight. The session affinity layer uses it so a binding to a
// credential whose weight became zero is not served again.
func (s *PipelineSelector) weightedCandidates(ctx context.Context, provider string, auths []*Auth) []*Auth {
	if !slices.Contains(activeSlots(s.selection(ctx)), config.SelectionWeightedRoundRobin) {
		return auths
	}
	preferred := preferredAvailableAuth(ctx, provider, auths)
	kept := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if auth == preferred || authWeight(auth) > 0 {
			kept = append(kept, auth)
		}
	}
	return kept
}

// Pick selects one credential for a new selection.
func (s *PipelineSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := s.now()
	candidates := auths
	if !pipelineCandidatesPrefiltered(ctx) || len(auths) == 0 {
		available, errAvailable := availableForPipeline(ctx, auths, provider, model, now)
		if errAvailable != nil {
			return nil, errAvailable
		}
		candidates = available
	}
	if preferred := preferredAvailableAuth(ctx, provider, candidates); preferred != nil {
		logRoutingProfileSelection(ctx, routingStagePreferred, preferred)
		return preferred, nil
	}
	slots := activeSlots(s.selection(ctx))
	for _, slot := range slots {
		if slot != config.SelectionWeightedRoundRobin {
			continue
		}
		// Zero-weight credentials are never chosen by weighted round-robin, so they
		// must not win a narrowing step either. An explicit preference was already
		// taken above and wins over a zero weight.
		candidates = positiveWeightAuths(candidates)
		if len(candidates) == 0 {
			return nil, &Error{Code: "auth_unavailable", Message: "no auth available with positive weight"}
		}
	}
	picker := config.SelectionRoundRobin
	group := candidates
	for _, slot := range slots {
		if config.IsPickerSlot(slot) {
			picker = slot
			break
		}
		group = s.narrow(ctx, slot, group, model, now)
	}
	selected, errPick := s.pick(ctx, picker, provider, model, opts, group)
	if errPick == nil {
		logRoutingProfileSelection(ctx, routingStagePipeline, selected)
	}
	return selected, errPick
}

// narrow applies one narrowing slot. A selection that skips the quota reserve, such
// as one serving a pinned credential, is not narrowed by reserve levels.
func (s *PipelineSelector) narrow(ctx context.Context, slot string, group []*Auth, model string, now time.Time) []*Auth {
	switch slot {
	case config.SelectionReserves:
		if quotaReserveSkipped(ctx) {
			return group
		}
		return narrowByReserves(group, model, now)
	case config.SelectionPriority:
		return narrowByPriority(group, model, now)
	case config.SelectionEarliestReset:
		return narrowByEarliestReset(group, model, now)
	case config.SelectionMostRemaining:
		return narrowByMostRemaining(group, model, now)
	default:
		return group
	}
}

// pick delegates to the legacy selector of a picker slot. The picker-only context
// keeps it from reapplying reserve, tier, or preference narrowing.
func (s *PipelineSelector) pick(ctx context.Context, slot, provider, model string, opts cliproxyexecutor.Options, group []*Auth) (*Auth, error) {
	pickerCtx := context.WithValue(withPickerOnly(ctx), prevalidatedAuthCandidatesKey{}, true)
	switch slot {
	case config.SelectionWeightedRoundRobin:
		return s.weighted.Pick(pickerCtx, provider, model, opts, group)
	case config.SelectionFillFirst:
		return s.fillFirst.Pick(pickerCtx, provider, model, opts, group)
	default:
		return s.roundRobin.Pick(pickerCtx, provider, model, opts, group)
	}
}

// narrowByReserves keeps the lowest active soft reserve level. Hard-reserved
// credentials are already removed from pipeline candidates; should only those
// remain, the group is kept as it is.
func narrowByReserves(auths []*Auth, _ string, now time.Time) []*Auth {
	selectable, _, _ := quotaReserveFilter(auths, now)
	if len(selectable) == 0 {
		return auths
	}
	return selectable
}

// narrowByPriority keeps the highest priority tier.
func narrowByPriority(auths []*Auth, _ string, _ time.Time) []*Auth {
	return highestPriorityAuths(auths)
}

// narrowByEarliestReset keeps the credentials sharing the earliest known future
// reset. Credentials without a known reset are dropped only when one is known.
func narrowByEarliestReset(auths []*Auth, model string, now time.Time) []*Auth {
	if group := earliestResetGroup(auths, model, now); len(group) > 0 {
		return group
	}
	return auths
}

// narrowByMostRemaining keeps the credentials with the highest minimum remaining
// percent. Credentials without usable windows are dropped only when one is known.
func narrowByMostRemaining(auths []*Auth, _ string, now time.Time) []*Auth {
	var group []*Auth
	var best float64
	for _, candidate := range auths {
		remaining, ok := subscriptionRemainingForAuth(candidate, now)
		if !ok {
			continue
		}
		switch {
		case group == nil || remaining > best:
			group, best = []*Auth{candidate}, remaining
		case remaining == best:
			group = append(group, candidate)
		}
	}
	if len(group) == 0 {
		return auths
	}
	return group
}
