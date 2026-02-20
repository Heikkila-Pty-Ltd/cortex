package temporal

import (
	"log"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
)

// StartWorker connects to Temporal and starts the cortex task queue worker.
// The store and tiers are injected so activities can record outcomes and resolve agents.
func StartWorker(st *store.Store, tiers config.Tiers) error {
	c, err := client.Dial(client.Options{
		HostPort: "127.0.0.1:7233",
	})
	if err != nil {
		return err
	}
	defer c.Close()

	w := worker.New(c, "cortex-task-queue", worker.Options{})

	acts := &Activities{Store: st, Tiers: tiers}

	// --- Core Workflows ---
	w.RegisterWorkflow(CortexAgentWorkflow)
	w.RegisterWorkflow(PlanningCeremonyWorkflow)

	// --- CHUM Workflows ---
	w.RegisterWorkflow(ContinuousLearnerWorkflow)
	w.RegisterWorkflow(TacticalGroomWorkflow)
	w.RegisterWorkflow(StrategicGroomWorkflow)

	// --- Core Activities ---
	w.RegisterActivity(acts.StructuredPlanActivity)
	w.RegisterActivity(acts.ExecuteActivity)
	w.RegisterActivity(acts.CodeReviewActivity)
	w.RegisterActivity(acts.DoDVerifyActivity)
	w.RegisterActivity(acts.RecordOutcomeActivity)
	w.RegisterActivity(acts.EscalateActivity)
	w.RegisterActivity(acts.GroomBacklogActivity)
	w.RegisterActivity(acts.GenerateQuestionsActivity)
	w.RegisterActivity(acts.SummarizePlanActivity)

	// --- CHUM Learner Activities ---
	w.RegisterActivity(acts.ExtractLessonsActivity)
	w.RegisterActivity(acts.StoreLessonActivity)
	w.RegisterActivity(acts.GenerateSemgrepRuleActivity)
	w.RegisterActivity(acts.RunSemgrepScanActivity)

	// --- CHUM Groom Activities ---
	w.RegisterActivity(acts.MutateBeadsActivity)
	w.RegisterActivity(acts.GenerateRepoMapActivity)
	w.RegisterActivity(acts.GetBeadStateSummaryActivity)
	w.RegisterActivity(acts.StrategicAnalysisActivity)
	w.RegisterActivity(acts.GenerateMorningBriefingActivity)

	log.Println("Temporal Worker started on cortex-task-queue...")
	return w.Run(worker.InterruptCh())
}
