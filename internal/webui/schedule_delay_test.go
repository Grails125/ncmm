package webui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScheduleRandomDelayValidationAndPersistence(t *testing.T) {
	manager := newControlledRunManager(t, 1)
	store, err := newWebConfigStore(filepath.Join(manager.home, "webui.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := newScheduler(context.Background(), store, manager)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.close()
	for _, seconds := range []int{-1, 86401} {
		_, err := scheduler.upsert(Schedule{Name: "invalid", Command: "sign", Cron: "30 8 * * *", RandomDelaySeconds: seconds})
		if err == nil || !strings.Contains(err.Error(), "随机延迟") {
			t.Fatalf("delay %d validation = %v", seconds, err)
		}
	}
	for _, seconds := range []int{0, 86400} {
		job, err := scheduler.upsert(Schedule{Name: "valid", Command: "sign", Cron: "30 8 * * *", RandomDelaySeconds: seconds})
		if err != nil {
			t.Fatal(err)
		}
		reloaded, err := newWebConfigStore(store.path)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, persisted := range reloaded.snapshot().Jobs {
			if persisted.ID == job.ID {
				found = persisted.RandomDelaySeconds == seconds
			}
		}
		if !found {
			t.Fatalf("delay %d was not preserved after reload", seconds)
		}
	}
}

func TestSchedulerCronDelayAndImmediateManualRun(t *testing.T) {
	manager := newControlledRunManager(t, 1)
	defer manager.supervisor.Close(time.Second)
	manager.randomDelay = func(max int) int { return max }
	store, err := newWebConfigStore(filepath.Join(manager.home, "webui.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(manager.home, "manual.done")
	job := controlledJob("delayed", "skip", "account.json", gate)
	job.Cron, job.Enabled, job.RandomDelaySeconds = "30 8 * * *", true, 3600
	if _, err := store.upsert(job); err != nil {
		t.Fatal(err)
	}
	scheduler, err := newScheduler(context.Background(), store, manager)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.close()
	scheduler.cron.Entries()[0].Job.Run()
	records := manager.list()
	if len(records) != 1 || records[0].Status != "queued" || records[0].DelaySeconds != 3600 || records[0].NotBefore == nil {
		t.Fatalf("cron should wait before execution: %+v", records)
	}
	if manager.runningCount() != 0 || manager.queuedCount() != 1 {
		t.Fatal("delayed run consumed an execution slot")
	}
	if err := manager.stop(records[0].ID); err != nil {
		t.Fatal(err)
	}
	manual, err := scheduler.runNow(job.ID)
	if err != nil || manual.Status != "running" || manual.NotBefore != nil || manual.DelaySeconds != 0 {
		t.Fatalf("manual run should bypass random delay: %+v, %v", manual, err)
	}
	releaseRun(t, gate)
	waitForRunStatus(t, manager, manual.ID, "success")
}

func TestDelayedRunAllowsOtherJobsAndRejectsOverlap(t *testing.T) {
	manager := newControlledRunManager(t, 1)
	defer manager.supervisor.Close(time.Second)
	manager.randomDelay = func(max int) int { return max }
	job := controlledJob("delayed", "skip", "shared.json", filepath.Join(manager.home, "delayed.done"))
	job.RandomDelaySeconds = 3600
	delayed, err := manager.startScheduled(context.Background(), job)
	if err != nil || delayed.Status != "queued" {
		t.Fatalf("delayed start = %+v, %v", delayed, err)
	}
	duplicate, err := manager.startScheduled(context.Background(), job)
	if err != nil || duplicate.Status != "skipped" {
		t.Fatalf("waiting run must prevent overlap: %+v, %v", duplicate, err)
	}
	gate := filepath.Join(manager.home, "other.done")
	other, err := manager.start(context.Background(), controlledJob("other", "skip", "shared.json", gate))
	if err != nil || other.Status != "running" {
		t.Fatalf("delay should not reserve account or execution slot: %+v, %v", other, err)
	}
	if err := manager.deleteRun(delayed.ID); err == nil {
		t.Fatal("active delayed record must not be deleted")
	}
	if err := manager.stop(delayed.ID); err != nil {
		t.Fatal(err)
	}
	stopped := waitForRunStatus(t, manager, delayed.ID, "stopped")
	if stopped.FinishedAt == nil || manager.queuedCount() != 0 {
		t.Fatal("stopping the delay did not remove its queued activity")
	}
	releaseRun(t, gate)
	waitForRunStatus(t, manager, other.ID, "success")
	if log, err := manager.readLog(delayed.ID); err != nil || !strings.Contains(string(log), "随机延迟") {
		t.Fatalf("delay should be explained in logs: %q, %v", log, err)
	}
}

func TestRandomDelayExpiresBeforeExecution(t *testing.T) {
	manager := newControlledRunManager(t, 1)
	defer manager.supervisor.Close(time.Second)
	manager.randomDelay = func(max int) int { return max }
	gate := filepath.Join(manager.home, "ready.done")
	releaseRun(t, gate)
	job := controlledJob("delayed", "skip", "account.json", gate)
	job.RandomDelaySeconds = 1
	delayed, err := manager.startScheduled(context.Background(), job)
	if err != nil || delayed.Status != "queued" || delayed.NotBefore == nil {
		t.Fatalf("delayed start = %+v, %v", delayed, err)
	}
	finished := waitForRunStatus(t, manager, delayed.ID, "success")
	if finished.StartedAt.Before(*delayed.NotBefore) || manager.queuedCount() != 0 {
		t.Fatalf("execution started before delay elapsed: %+v", finished)
	}
}

func TestRandomDelayCanSelectZero(t *testing.T) {
	manager := newControlledRunManager(t, 1)
	defer manager.supervisor.Close(time.Second)
	manager.randomDelay = func(max int) int { return 0 }
	gate := filepath.Join(manager.home, "zero.done")
	job := controlledJob("zero", "skip", "account.json", gate)
	job.RandomDelaySeconds = 86400
	record, err := manager.startScheduled(context.Background(), job)
	if err != nil || record.Status != "running" || record.NotBefore != nil || record.DelaySeconds != 0 {
		t.Fatalf("zero delay should execute immediately: %+v, %v", record, err)
	}
	releaseRun(t, gate)
	waitForRunStatus(t, manager, record.ID, "success")
}

func TestExpiredDelayStillWaitsForAccountResource(t *testing.T) {
	manager := newControlledRunManager(t, 2)
	defer manager.supervisor.Close(time.Second)
	manager.randomDelay = func(max int) int { return max }
	activeGate := filepath.Join(manager.home, "active.done")
	active, err := manager.start(context.Background(), controlledJob("active", "skip", "shared.json", activeGate))
	if err != nil || active.Status != "running" {
		t.Fatalf("active start = %+v, %v", active, err)
	}
	delayedGate := filepath.Join(manager.home, "delayed.done")
	job := controlledJob("delayed", "skip", "shared.json", delayedGate)
	job.RandomDelaySeconds = 1
	delayed, err := manager.startScheduled(context.Background(), job)
	if err != nil || delayed.NotBefore == nil {
		t.Fatalf("delayed start = %+v, %v", delayed, err)
	}
	time.Sleep(time.Until(*delayed.NotBefore) + 25*time.Millisecond)
	if record, _ := manager.get(delayed.ID); record.Status != "queued" || manager.runningCount() != 1 {
		t.Fatalf("expired delay bypassed shared account protection: %+v", record)
	}
	releaseRun(t, activeGate)
	finished := waitForRunStatus(t, manager, active.ID, "success")
	started := waitForRunStatus(t, manager, delayed.ID, "running")
	if started.StartedAt.Before(*finished.FinishedAt) {
		t.Fatal("delayed task began before the shared account was released")
	}
	releaseRun(t, delayedGate)
	waitForRunStatus(t, manager, delayed.ID, "success")
}

func TestSchedulerCloseCancelsDelayedRuns(t *testing.T) {
	manager := newControlledRunManager(t, 1)
	manager.randomDelay = func(max int) int { return max }
	store, err := newWebConfigStore(filepath.Join(manager.home, "webui.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := newScheduler(context.Background(), store, manager)
	if err != nil {
		t.Fatal(err)
	}
	job := controlledJob("delayed", "skip", "account.json", filepath.Join(manager.home, "never.done"))
	job.RandomDelaySeconds = 3600
	record, err := manager.startScheduled(scheduler.ctx, job)
	if err != nil || record.Status != "queued" {
		t.Fatalf("delayed start = %+v, %v", record, err)
	}
	scheduler.close()
	waitForRunStatus(t, manager, record.ID, "stopped")
	if manager.queuedCount() != 0 || manager.runningCount() != 0 {
		t.Fatal("scheduler shutdown left delayed activity behind")
	}
}
