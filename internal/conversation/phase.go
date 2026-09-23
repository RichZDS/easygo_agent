package conversation

import "sort"

const phasePageLimit = 100

func cloneRunPhase(phase RunPhase) RunPhase {
	if phase.DurationMS != nil {
		duration := *phase.DurationMS
		phase.DurationMS = &duration
	}
	return phase
}

// pagePhases returns at most 100 rows whose sequence is greater than after.
// Rows are chosen in (sequence, execution_id) order so the sequence cursor does
// not repeat, then presented in (execution_id, sequence) order. A sequence
// shared by more than one execution can be split by the 100-row cap; the cursor
// is still that sequence, because afterSequence keeps only greater sequences.
func pagePhases(phases []RunPhase, afterSequence int64) RunPhasePage {
	selected := make([]RunPhase, 0)
	for _, phase := range phases {
		if phase.Sequence > afterSequence {
			selected = append(selected, cloneRunPhase(phase))
		}
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].Sequence != selected[j].Sequence {
			return selected[i].Sequence < selected[j].Sequence
		}
		return selected[i].ExecutionID < selected[j].ExecutionID
	})
	if len(selected) > phasePageLimit {
		trimmed := make([]RunPhase, phasePageLimit)
		copy(trimmed, selected[:phasePageLimit])
		selected = trimmed
	}
	next := afterSequence
	for _, phase := range selected {
		if phase.Sequence > next {
			next = phase.Sequence
		}
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].ExecutionID != selected[j].ExecutionID {
			return selected[i].ExecutionID < selected[j].ExecutionID
		}
		return selected[i].Sequence < selected[j].Sequence
	})
	return RunPhasePage{Phases: selected, NextAfter: next}
}
