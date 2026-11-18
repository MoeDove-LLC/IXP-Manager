package main

import (
	"fmt"
	"strconv"

	"github.com/moedove/ixp-manager/internal/routeros/control"
)

const cleanupAwaitingContinuation = "cleanup-awaiting-explicit-continuation"

func requestBatchLimit(action string) int {
	switch action {
	case "cleanup":
		return control.MaxCleanupRequestBatches
	case "discard-prepared":
		return control.MaxDiscardRequestBatches
	default:
		return 0
	}
}

func annotateCleanupResponse(result *response, socket string, input request) {
	switch input.Action {
	case "cleanup", "status", "confirm":
		result.ContinueCommand = nil
	default:
		return
	}
	status, progress := result.Status, result.Status.Cleanup
	if !result.OK || result.Error != "" || status.Phase != control.Confirmed || !status.ConfirmationVerified || status.RollbackVerified ||
		!canonicalHex(status.TransactionID, 32) || !canonicalHex(status.PlanChecksum, 64) || progress.Total < 1 ||
		progress.Completed < 0 || progress.Completed > progress.Total {
		return
	}
	if input.Action != "status" && (status.TransactionID != input.TransactionID || status.PlanChecksum != input.Checksum) {
		return
	}
	if input.Action == "cleanup" {
		if input.MaxBatches == nil || *input.MaxBatches < 1 || *input.MaxBatches > control.MaxCleanupRequestBatches || progress.Completed < *input.MaxBatches {
			// Preserve the legacy receipt and require at least one verified operation per bounded work unit.
			return
		}
	}
	if progress.Phase != control.CleanupPending || progress.LastCode != cleanupAwaitingContinuation || progress.Completed >= progress.Total {
		return
	}
	result.Message = fmt.Sprintf("已验证 %d/%d，已确认版本的清理尚未完成，等待手动继续。", progress.Completed, progress.Total)
	if socket != "" && input.Action == "cleanup" {
		result.ContinueCommand = []string{"ixp-routeros", "cleanup", "--socket", socket, "--id", status.TransactionID,
			"--checksum", status.PlanChecksum, "--max-batches", strconv.Itoa(*input.MaxBatches)}
	}
}
