package app

import (
	"encoding/json"
	"testing"
)

func TestBuildRestartOTATaskDataMergesOnlyExpectedFields(t *testing.T) {
	currentJSON := `{
		"source_baseline_version":"5.2.1",
		"target_baseline_version":null,
		"predict_upgrade_duration":10,
		"preconditions_config":{"foo":"bar"},
		"overall_state":{"stage":"Terminate","state":"Idle"},
		"download_state":{"download_type":0,"percents":100,"stage":"Complete","fail_info":"timeout","fail_reason":3},
		"packages_info":[{"ecu":"VCU","version":"1","display_version":"1","downloaded_size":100,"file_size":200,"flash_finish":true,"file":"a","upgrade_spec_file":"b","start_flash_time":9,"end_flash_time":10}],
		"flash_state":{"failure_reason":"boom"}
	}`

	backupJSON := `{
		"source_baseline_version":"5.2.1",
		"target_baseline_version":"6.0.5",
		"predict_upgrade_duration":134,
		"preconditions_config":{"foo":"bar","baz":1},
		"schedule_state":{"stage":"Idle","set_time":0},
		"packages_info":[{"ecu":"VCU","version":"1","display_version":"1","downloaded_size":19,"file_size":200,"flash_finish":true,"file":"a","upgrade_spec_file":"b","start_flash_time":1,"end_flash_time":2,"start_rollback_time":3,"end_rollback_time":4}],
		"expire_time":1753861486
	}`

	mergedJSON, mergedTaskData, err := buildRestartOTATaskData(currentJSON, backupJSON)
	if err != nil {
		t.Fatalf("buildRestartOTATaskData returned error: %v", err)
	}

	if mergedTaskData.TargetBaselineVersion != "6.0.5" {
		t.Fatalf("unexpected target_baseline_version: %q", mergedTaskData.TargetBaselineVersion)
	}

	if mergedTaskData.OverallState.Stage != overallStageDownload || mergedTaskData.OverallState.State != overallStateProcess {
		t.Fatalf("unexpected overall_state: %+v", mergedTaskData.OverallState)
	}

	if mergedTaskData.DownloadState.Stage != downloadStageRetrieve || mergedTaskData.DownloadState.Percents != 0 {
		t.Fatalf("unexpected download_state: %+v", mergedTaskData.DownloadState)
	}

	if len(mergedTaskData.PackagesInfo) != 1 {
		t.Fatalf("unexpected packages_info length: %d", len(mergedTaskData.PackagesInfo))
	}

	if mergedTaskData.PackagesInfo[0].DownloadedSize != 0 {
		t.Fatalf("expected package progress to be reset, got %d", mergedTaskData.PackagesInfo[0].DownloadedSize)
	}

	var mergedMap map[string]any
	if err := json.Unmarshal([]byte(mergedJSON), &mergedMap); err != nil {
		t.Fatalf("failed to unmarshal merged JSON: %v", err)
	}

	if _, ok := mergedMap["flash_state"]; ok {
		t.Fatal("flash_state must be removed")
	}

	if v, ok := mergedMap["target_baseline_version"].(string); !ok || v != "6.0.5" {
		t.Fatalf("unexpected target_baseline_version in merged JSON: %#v", mergedMap["target_baseline_version"])
	}

	overallState, ok := mergedMap["overall_state"].(map[string]any)
	if !ok || overallState["stage"] != "Download" || overallState["state"] != "Process" {
		t.Fatalf("unexpected overall_state in merged JSON: %#v", mergedMap["overall_state"])
	}

	downloadState, ok := mergedMap["download_state"].(map[string]any)
	if !ok || downloadState["stage"] != "Retrive Packages" {
		t.Fatalf("unexpected download_state in merged JSON: %#v", mergedMap["download_state"])
	}

	packagesInfo, ok := mergedMap["packages_info"].([]any)
	if !ok || len(packagesInfo) != 1 {
		t.Fatalf("unexpected packages_info in merged JSON: %#v", mergedMap["packages_info"])
	}

	pkg, ok := packagesInfo[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected package type: %T", packagesInfo[0])
	}

	if downloaded, ok := pkg["downloaded_size"].(float64); !ok || downloaded != 0 {
		t.Fatalf("expected downloaded_size to be reset, got %#v", pkg["downloaded_size"])
	}

	if flashFinish, ok := pkg["flash_finish"].(bool); !ok || flashFinish {
		t.Fatalf("expected flash_finish to be false, got %#v", pkg["flash_finish"])
	}
}
