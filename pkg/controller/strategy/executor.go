package strategy

import (
	"fmt"
	"strconv"

	"github.com/golang/glog"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"

	"github.com/bookingcom/shipper/pkg/apis/shipper/v1"
	"github.com/bookingcom/shipper/pkg/controller"
)

type releaseInfo struct {
	release            *v1.Release
	installationTarget *v1.InstallationTarget
	trafficTarget      *v1.TrafficTarget
	capacityTarget     *v1.CapacityTarget
}

type Executor struct {
	contender *releaseInfo
	incumbent *releaseInfo
	recorder  record.EventRecorder
}

func (s *Executor) info(format string, args ...interface{}) {
	glog.Infof("Release %q: %s", controller.MetaKey(s.contender.release), fmt.Sprintf(format, args...))
}

func (s *Executor) event(obj runtime.Object, format string, args ...interface{}) {
	s.recorder.Eventf(
		obj,
		corev1.EventTypeNormal,
		"StrategyApplied",
		format,
		args,
	)
}

// execute executes the strategy. It returns an ExecutorResult, if a patch should
// be performed into some of the associated Release objects and an error if an error
// has happened. Currently if both values are nil it means that the operation was
// successful but no modifications are required.
func (s *Executor) execute() ([]ExecutorResult, error) {

	strategy := getStrategy(string(s.contender.release.Environment.ShipmentOrder.Strategy))
	targetStep := uint(s.contender.release.Spec.TargetStep)

	strategyStep, err := getStrategyStep(strategy, int(targetStep))
	if err != nil {
		return nil, err
	}

	//////////////////////////////////////////////////////////////////////////
	// Installation
	//
	if canContinue := checkInstallation(s.contender); !canContinue {
		s.info("installation pending")
		return nil, nil
	} else {
		s.info("installation finished")
	}

	// Contender
	if contenderReady, contenderPatches, err := s.checkContender(strategyStep); err != nil {
		return nil, err
	} else if !contenderReady {
		s.info("contender is not yet ready for release %q, step %d", s.contender.release.Name, targetStep)
		return contenderPatches, nil
	} else {
		s.info("contender is ready for release %q, step %d", s.contender.release.Name, targetStep)
	}

	// Incumbent
	if s.incumbent != nil {
		if incumbentReady, incumbentPatches, err := s.checkIncumbent(strategyStep); err != nil {
			return nil, err
		} else if !incumbentReady {
			s.info("incumbent is not yet ready for release %q, step %d", s.incumbent.release.Name, targetStep)
			return incumbentPatches, nil
		} else {
			s.info("incumbent is ready for step release %q, step %d", s.incumbent.release.Name, targetStep)
		}
	} else {
		s.info("no incumbent, must be a new app")
	}

	//////////////////////////////////////////////////////////////////////////
	// Release
	//
	lastStepIndex := len(strategy.Spec.Steps) - 1
	if lastStepIndex < 0 {
		lastStepIndex = 0
	}

	isLastStep := targetStep == uint(lastStepIndex)

	if releasePatches, err := s.finalizeRelease(targetStep, strategyStep, isLastStep); err != nil {
		return nil, err
	} else {
		s.event(s.contender.release, "step %d finished", targetStep)
		return releasePatches, nil
	}
}

func (s *Executor) finalizeRelease(targetStep uint, strategyStep v1.StrategyStep, isLastStep bool) ([]ExecutorResult, error) {
	var contenderPhase string

	if isLastStep {
		contenderPhase = v1.ReleasePhaseInstalled
	} else {
		contenderPhase = v1.ReleasePhaseWaitingForCommand
	}

	var releasePatches []ExecutorResult

	reportedStep := s.contender.release.Status.AchievedStep
	reportedPhase := s.contender.release.Status.Phase

	if targetStep != reportedStep || contenderPhase != reportedPhase {
		contenderStatus := &v1.ReleaseStatus{
			AchievedStep: targetStep,
			Phase:        contenderPhase,
		}
		releasePatches = append(releasePatches, &ReleaseUpdateResult{
			NewStatus: contenderStatus,
			Name:      s.contender.release.Name,
		})
	}

	if s.incumbent != nil {
		incumbentPhase := v1.ReleasePhaseInstalled
		if isLastStep {
			incumbentPhase = v1.ReleasePhaseSuperseded
		}

		if incumbentPhase != s.incumbent.release.Status.Phase {
			incumbentStatus := &v1.ReleaseStatus{
				Phase:        incumbentPhase,
				AchievedStep: s.incumbent.release.Status.AchievedStep,
			}
			releasePatches = append(releasePatches, &ReleaseUpdateResult{
				NewStatus: incumbentStatus,
				Name:      s.incumbent.release.Name,
			})
		}
	}

	return releasePatches, nil
}

func (s *Executor) checkContender(strategyStep v1.StrategyStep) (bool, []ExecutorResult, error) {
	var patches []ExecutorResult

	capacity, err := strconv.Atoi(strategyStep.ContenderCapacity)
	if err != nil {
		return false, nil, err
	}

	trafficWeight, err := strconv.Atoi(strategyStep.ContenderTraffic)
	if err != nil {
		return false, nil, err
	}

	if achieved, newSpec := checkCapacity(s.contender.capacityTarget, uint(capacity), contenderCapacityComparison); !achieved {
		s.info("contender %q hasn't achieved capacity yet", s.contender.release.Name)
		if newSpec != nil {
			patches = append(patches, &CapacityTargetOutdatedResult{
				NewSpec: newSpec,
				Name:    s.contender.release.Name,
			})
			return false, patches, nil
		} else {
			return false, nil, nil
		}
	} else {
		s.info("contender %q has achieved capacity", s.contender.release.Name)
	}

	if achieved, newSpec := checkTraffic(s.contender.trafficTarget, uint(trafficWeight), contenderTrafficComparison); !achieved {
		s.info("contender %q hasn't achieved traffic yet", s.contender.release.Name)
		if newSpec != nil {
			patches = append(patches, &TrafficTargetOutdatedResult{
				NewSpec: newSpec,
				Name:    s.contender.release.Name,
			})
			return false, patches, nil
		} else {
			return false, nil, nil
		}
	} else {
		s.info("contender %q has achieved traffic", s.contender.release.Name)
	}

	return true, nil, nil
}

func (s *Executor) checkIncumbent(strategyStep v1.StrategyStep) (bool, []ExecutorResult, error) {
	var patches []ExecutorResult

	capacity, err := strconv.Atoi(strategyStep.IncumbentCapacity)
	if err != nil {
		return false, nil, err
	}

	trafficWeight, err := strconv.Atoi(strategyStep.IncumbentTraffic)
	if err != nil {
		return false, nil, err
	}

	if achieved, newSpec := checkTraffic(s.incumbent.trafficTarget, uint(trafficWeight), incumbentTrafficComparison); !achieved {
		s.info("incumbent %q hasn't achieved traffic yet", s.incumbent.release.Name)
		if newSpec != nil {
			patches = append(patches, &TrafficTargetOutdatedResult{
				NewSpec: newSpec,
				Name:    s.incumbent.release.Name,
			})
			return false, patches, nil
		} else {
			return false, nil, nil
		}
	} else {
		s.info("incumbent %q has achieved traffic", s.incumbent.release.Name)
	}

	if achieved, newSpec := checkCapacity(s.incumbent.capacityTarget, uint(capacity), incumbentCapacityComparison); !achieved {
		s.info("incumbent %q hasn't achieved capacity yet", s.incumbent.release.Name)
		if newSpec != nil {
			patches = append(patches, &CapacityTargetOutdatedResult{
				NewSpec: newSpec,
				Name:    s.incumbent.release.Name,
			})
			return false, patches, nil
		} else {
			return false, nil, nil
		}
	} else {
		s.info("incumbent %q has achieved capacity", s.incumbent.release.Name)
	}

	return true, nil, nil
}