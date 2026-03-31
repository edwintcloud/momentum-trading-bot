package main

import (
	"log"
	"os"

	"github.com/joho/godotenv"

	"github.com/edwintcloud/momentum-trading-bot/cmd"
)

func main() {
	_ = godotenv.Load()

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "backtest":
			if err := cmd.RunBacktest(os.Args[2:]); err != nil {
				log.Fatalf("backtest: %v", err)
			}
			return
		case "batch-backtest":
			if err := cmd.RunBatchBacktest(os.Args[2:]); err != nil {
				log.Fatalf("batch-backtest: %v", err)
			}
			return
		case "optimize":
			if err := cmd.RunOptimize(os.Args[2:]); err != nil {
				log.Fatalf("optimize: %v", err)
			}
			return
		case "auto-optimize":
			if err := cmd.RunAutoOptimize(os.Args[2:]); err != nil {
				log.Fatalf("auto-optimize: %v", err)
			}
			return
		case "label-candidates":
			if err := cmd.RunLabelCandidates(os.Args[2:]); err != nil {
				log.Fatalf("label-candidates: %v", err)
			}
			return
		case "train-ml":
			if err := cmd.RunTrainML(os.Args[2:]); err != nil {
				log.Fatalf("train-ml: %v", err)
			}
			return
		case "prepare-ml-dataset":
			if err := cmd.RunPrepareMLDataset(os.Args[2:]); err != nil {
				log.Fatalf("prepare-ml-dataset: %v", err)
			}
			return
		case "auto-train-ml":
			if err := cmd.RunAutoTrainML(os.Args[2:]); err != nil {
				log.Fatalf("auto-train-ml: %v", err)
			}
			return
		case "live":
			cmd.RunLiveTrading()
			return
		}
	}

	cmd.RunLiveTrading()
}
