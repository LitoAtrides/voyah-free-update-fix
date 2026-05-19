package app

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type otaTaskData struct {
	TaskID                 int               `json:"task_id"`
	TaskType               int               `json:"task_type"`
	SessionID              string            `json:"session_id"`
	UpgradeType            int               `json:"upgrade_type"`
	SourceBaselineVersion  string            `json:"source_baseline_version"`
	TargetBaselineVersion  string            `json:"target_baseline_version"`
	PredictUpgradeDuration int               `json:"predict_upgrade_duration"`
	RetryUpgrade           bool              `json:"retry_upgrade"`
	Rollback               bool              `json:"rollback"`
	OverallState           otaOverallState   `json:"overall_state"`
	DownloadState          otaDownloadState  `json:"download_state"`
	FlashState             *otaFlashState    `json:"flash_state,omitempty"`
	PackagesInfo           []otaPackageInfo  `json:"packages_info"`
	ScheduleState          *otaScheduleState `json:"schedule_state,omitempty"`
	ExpireTime             *otaExpireTime    `json:"expire_time,omitempty"`
}

type otaOverallState struct {
	Stage string `json:"stage"`
	State string `json:"state"`
}

type otaScheduleState struct {
	SetTime int64  `json:"set_time"`
	Stage   string `json:"stage"`
}

type otaExpireTime int64

type otaDownloadState struct {
	DownloadType int    `json:"download_type"`
	FailInfo     string `json:"fail_info"`
	Percents     int    `json:"percents"`
	Stage        string `json:"stage"`
}

type otaFlashState struct {
	FailureReason          string `json:"failure_reason"`
	FailureReasonExtraInfo string `json:"failure_reason_extra_info"`
}

type otaPackageInfo struct {
	ECU                     string `json:"ecu"`
	Version                 string `json:"version"`
	DisplayVersion          string `json:"display_version"`
	File                    string `json:"file"`
	UpgradeSpecFile         string `json:"upgrade_spec_file"`
	DownloadedSize          int64  `json:"downloaded_size"`
	FileSize                int64  `json:"file_size"`
	FlashFinish             bool   `json:"flash_finish"`
	DiffPackage             bool   `json:"diff_package"`
	MaxUpgradePercent       int    `json:"max_upgrade_percent"`
	UpdateSequence          int    `json:"update_sequence"`
	ParallelUpgradeSequence int    `json:"parallel_upgrade_sequence"`
}

type packageRow struct {
	ECU               string
	Version           string
	Downloaded        int64
	Total             int64
	Percent           int
	Status            string
	MaxPercent        int
	ParallelSeq       int
	FileExists        bool
	UpgradeSpecExists bool
	OriginalIndex     int
}

type packageStats struct {
	DownloadedCount int
	PartialCount    int
	PendingCount    int
	FlashedCount    int
	DiffCount       int
	TotalDownloaded int64
	TotalSize       int64
	TotalPercent    int
}

type textStyle struct {
	enabled bool
}

var packagesInfoKeyRegexp = regexp.MustCompile(`"packages_info"\s*:\s*`)

//nolint:misspell
const (
	packageStatusFlashed     = "flashed"
	packageStatusDownloaded  = "downloaded"
	packageStatusDownloading = "downloading"
	packageStatusPending     = "pending"
	maxPercentValue          = 100
	packageStatusWidth       = 11
	existsValueWidth         = 10
	percentHighThreshold     = 90
	percentMediumThreshold   = 50
	byteUnitBase             = 1024
	downloadStageRetrieve    = "Retrive Packages"
	downloadStageComplete    = "Complete"
	overallStageDownload     = "Download"
	overallStageTerminate    = "Terminate"
	overallStateProcess      = "Process"
	overallStateFailed       = "Failed"
	overallStateIdle         = "Idle"
)

func newTextStyle() textStyle {
	return textStyle{
		enabled: os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb",
	}
}

func (s textStyle) apply(text string, codes ...string) string {
	if !s.enabled || len(codes) == 0 {
		return text
	}

	return "\033[" + strings.Join(codes, ";") + "m" + text + "\033[0m"
}

func (s textStyle) bold(text string) string    { return s.apply(text, "1") }
func (s textStyle) italic(text string) string  { return s.apply(text, "3") }
func (s textStyle) cyan(text string) string    { return s.apply(text, "36") }
func (s textStyle) blue(text string) string    { return s.apply(text, "34") }
func (s textStyle) green(text string) string   { return s.apply(text, "32") }
func (s textStyle) yellow(text string) string  { return s.apply(text, "33") }
func (s textStyle) red(text string) string     { return s.apply(text, "31") }
func (s textStyle) magenta(text string) string { return s.apply(text, "35") }

func printTaskDataInfo(taskData otaTaskData, rows []packageRow, stats packageStats) {
	sty := newTextStyle()

	printTaskOverview(taskData, sty)
	printPackagesSummary(rows, stats, sty)
	printPackagesDetails(rows, sty)

	switch {
	case isFlashFailureCase(taskData):
		printFlashFailureStatus(rows, sty)
	case isDownloadProcessCase(taskData):
		printUpdateReadiness(rows, sty)
	default:
		fmt.Println(sty.bold(sty.cyan("=== Статус готовности ===")))
		fmt.Printf("%s\n",
			sty.red(sty.bold("Задача OTA находится в неподдерживаемом состоянии. Исправление context.db запустить нельзя.")),
		)
	}
}

func parseOTATaskData(raw string) (otaTaskData, error) {
	var parsed otaTaskData
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return otaTaskData{}, fmt.Errorf("некорректный JSON в ota_task.taskData: %w", err)
	}

	return parsed, nil
}

func buildPackageRowsAndStats(packages []otaPackageInfo, client tboxFileClient) ([]packageRow, packageStats) {
	rows := make([]packageRow, 0, len(packages))
	stats := packageStats{}

	for id, pkg := range packages {
		stats.TotalDownloaded += pkg.DownloadedSize
		stats.TotalSize += pkg.FileSize

		percent := packagePercent(pkg.DownloadedSize, pkg.FileSize)
		status := packageStatus(pkg)

		switch status {
		case packageStatusFlashed:
			stats.FlashedCount++
		case packageStatusDownloaded:
			stats.DownloadedCount++
		case packageStatusDownloading:
			stats.PartialCount++
		default:
			stats.PendingCount++
		}

		if pkg.DiffPackage {
			stats.DiffCount++
		}

		rows = append(rows, packageRow{
			ECU:               pkg.ECU,
			Version:           packageVersion(pkg),
			Downloaded:        pkg.DownloadedSize,
			Total:             pkg.FileSize,
			Percent:           percent,
			Status:            status,
			MaxPercent:        pkg.MaxUpgradePercent,
			ParallelSeq:       pkg.ParallelUpgradeSequence,
			FileExists:        fileExistsAndNotEmptyOnTBox(client, pkg.File),
			UpgradeSpecExists: fileExistsAndNotEmptyOnTBox(client, pkg.UpgradeSpecFile),
			OriginalIndex:     id,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].MaxPercent == rows[j].MaxPercent {
			return rows[i].ECU < rows[j].ECU
		}

		return rows[i].MaxPercent < rows[j].MaxPercent
	})

	stats.TotalPercent = packagePercent(stats.TotalDownloaded, stats.TotalSize)

	return rows, stats
}

func packagePercent(downloaded, total int64) int {
	if total <= 0 {
		return 0
	}

	percent := int(downloaded * 100 / total)
	if percent > maxPercentValue {
		return maxPercentValue
	}

	if percent < 0 {
		return 0
	}

	return percent
}

func packageStatus(pkg otaPackageInfo) string {
	if pkg.FlashFinish {
		return packageStatusFlashed
	}

	if pkg.FileSize > 0 && pkg.DownloadedSize >= pkg.FileSize {
		return packageStatusDownloaded
	}

	if pkg.DownloadedSize > 0 {
		return packageStatusDownloading
	}

	return packageStatusPending
}

func packageVersion(pkg otaPackageInfo) string {
	if pkg.DisplayVersion != "" {
		return pkg.DisplayVersion
	}

	return pkg.Version
}

func printTaskOverview(taskData otaTaskData, sty textStyle) {
	fmt.Println(sty.bold(sty.cyan("=== Информация об OTA-задаче ===")))
	fmt.Printf("%s: %s -> %s\n",
		sty.bold("Версия ПО"),
		sty.bold(taskData.SourceBaselineVersion),
		sty.bold(taskData.TargetBaselineVersion),
	)
	fmt.Printf("%s: %s %s\n",
		sty.bold("Общее состояние"),
		fmt.Sprintf("%s=%s", sty.italic("stage"), colorizeStage(taskData.OverallState.Stage, sty)),
		fmt.Sprintf("%s=%s", sty.italic("state"), colorizedOverallState(taskData.OverallState.State, sty)),
	)
	fmt.Printf("%s: %s %s\n",
		sty.bold("Состояние загрузки"),
		fmt.Sprintf("%s=%s", sty.italic("stage"), colorizeStage(taskData.DownloadState.Stage, sty)),
		fmt.Sprintf(
			"%s=%s",
			sty.italic("progress"),
			colorizeByPercent(fmt.Sprintf("%d%%", taskData.DownloadState.Percents), taskData.DownloadState.Percents, sty),
		),
	)
	if taskData.DownloadState.FailInfo != "" {
		fmt.Printf("%s: %s\n",
			sty.bold("Информация об ошибке загрузки"),
			sty.red(taskData.DownloadState.FailInfo),
		)
	}
	fmt.Printf("%s: %s\n",
		sty.bold("Прогнозируемая длительность обновления"),
		sty.bold(fmt.Sprintf("%d min", taskData.PredictUpgradeDuration)),
	)

	if taskData.ScheduleState != nil {
		if taskData.ScheduleState.Stage != "" {
			fmt.Printf("%s: %s\n",
				sty.bold("Стадия расписания"),
				sty.cyan(taskData.ScheduleState.Stage),
			)
		}

		if taskData.ScheduleState.SetTime > 0 {
			fmt.Printf("%s: %s\n",
				sty.bold("Время расписания"),
				sty.cyan(time.Unix(taskData.ScheduleState.SetTime, 0).Format("02.01.2006 15:04:05")),
			)
		}
	}

	if isFlashFailureCase(taskData) && taskData.FlashState != nil {
		fmt.Printf("%s: %s\n",
			sty.bold("Причина ошибки прошивки"),
			sty.red(taskData.FlashState.FailureReason),
		)
		fmt.Printf("%s: %s\n",
			sty.bold("Детали ошибки прошивки"),
			sty.red(taskData.FlashState.FailureReasonExtraInfo),
		)
	}

	if taskData.ExpireTime != nil && *taskData.ExpireTime > 0 {
		fmt.Printf("%s: %s\n",
			sty.bold("Время истечения"),
			sty.cyan(time.Unix(int64(*taskData.ExpireTime), 0).Format("02.01.2006 15:04:05")),
		)
	}

	fmt.Println()
}

func colorizeStage(stage string, sty textStyle) string {
	return sty.cyan(stage)
}

func colorizedOverallState(state string, sty textStyle) string {
	switch strings.ToLower(state) {
	case "success", "done", "finish", "finished":
		return sty.green(state)
	case "process", "processing", "progress":
		return sty.yellow(state)
	case "failed", "failure", "error":
		return sty.red(state)
	default:
		return sty.magenta(state)
	}
}

func printPackagesSummary(rows []packageRow, stats packageStats, sty textStyle) {
	fmt.Println(sty.bold(sty.cyan("=== Сводка по пакетам ===")))
	fmt.Printf("%s: %s=%s %s=%s %s=%s %s=%s %s=%s %s=%s\n",
		sty.bold("Пакеты"),
		sty.italic("всего"),
		sty.bold(strconv.Itoa(len(rows))),
		sty.italic("загружено"),
		sty.green(sty.bold(strconv.Itoa(stats.DownloadedCount))),
		sty.italic("загружается"),
		sty.yellow(sty.bold(strconv.Itoa(stats.PartialCount))),
		sty.italic("ожидает"),
		sty.red(sty.bold(strconv.Itoa(stats.PendingCount))),
		sty.italic("прошито"),
		sty.magenta(sty.bold(strconv.Itoa(stats.FlashedCount))),
		sty.italic("diff"),
		sty.bold(strconv.Itoa(stats.DiffCount)),
	)
	fmt.Printf("%s: %s / %s (%s)\n",
		sty.bold("Данные"),
		sty.bold(formatBytes(stats.TotalDownloaded)),
		sty.bold(formatBytes(stats.TotalSize)),
		sty.bold(colorizeByPercent(fmt.Sprintf("%d%%", stats.TotalPercent), stats.TotalPercent, sty)),
	)
	fmt.Println()
}

func printPackagesDetails(rows []packageRow, sty textStyle) {
	fmt.Println(sty.bold(sty.cyan("=== Детали по пакетам ===")))

	header := fmt.Sprintf("%-10s | %-16s | %-12s | %-12s | %-8s | %-11s | %-10s | %-10s | %-6s",
		"ECU", "Версия", "Загружено", "Всего", "Прогресс", "Статус", "Файл (enc)", "Файл (otx)", "Порог")

	fmt.Println(header)
	fmt.Println(strings.Repeat("-", len(header)))

	for _, row := range rows {
		progressText := fmt.Sprintf("%-8s", fmt.Sprintf("%d%%", row.Percent))
		stageLimitText := fmt.Sprintf("%-6s", fmt.Sprintf("%d%%", row.MaxPercent))
		statusText := stylePackageStatus(row.Status, packageStatusWidth, sty)
		fileExistsText := styleExistsValue(row.FileExists, existsValueWidth, sty)
		specExistsText := styleExistsValue(row.UpgradeSpecExists, existsValueWidth, sty)

		fmt.Printf("%-10s | %-16s | %-12s | %-12s | %s | %s | %s | %s | %s\n",
			row.ECU,
			row.Version,
			formatBytes(row.Downloaded),
			formatBytes(row.Total),
			colorizeByPercent(progressText, row.Percent, sty),
			statusText,
			fileExistsText,
			specExistsText,
			sty.blue(stageLimitText),
		)
	}

	fmt.Println()
}

func checkFixContextDB(rows []packageRow) (bool, []int) {
	exceptionalECUs := map[string]struct{}{
		iviMCUName: {},
		iviMPUName: {},
		tboxName:   {},
	}

	missingExceptional := make([]string, 0)
	missingRequired := make([]string, 0)
	indexesToDelete := make([]int, 0)

	for _, row := range rows {
		if isPackageReady(row) {
			continue
		}

		if _, ok := exceptionalECUs[row.ECU]; ok {
			missingExceptional = append(missingExceptional, row.ECU)
			indexesToDelete = append(indexesToDelete, row.OriginalIndex)

			continue
		}

		missingRequired = append(missingRequired, row.ECU)
	}

	return len(missingRequired) == 0 && len(missingExceptional) > 0, indexesToDelete
}

func isFixStateAllowed(taskData otaTaskData) bool {
	return isDownloadProcessCase(taskData) || isFlashFailureCase(taskData)
}

func isDownloadProcessCase(taskData otaTaskData) bool {
	return taskData.DownloadState.Stage == downloadStageRetrieve &&
		taskData.OverallState.Stage == overallStageDownload &&
		taskData.OverallState.State == overallStateProcess
}

func isFlashFailureCase(taskData otaTaskData) bool {
	return taskData.DownloadState.Stage == downloadStageComplete &&
		taskData.OverallState.Stage == overallStageTerminate &&
		(taskData.OverallState.State == overallStateFailed ||
			taskData.OverallState.State == overallStateIdle)
}

func printUpdateReadiness(rows []packageRow, sty textStyle) {
	missingExceptional, missingRequired := collectMissingECUs(rows)

	sort.Strings(missingExceptional)
	sort.Strings(missingRequired)

	fmt.Println(sty.bold(sty.cyan("=== Статус готовности ===")))

	if len(missingExceptional) == 0 && len(missingRequired) == 0 {
		fmt.Printf("%s\n%s\n",
			sty.green(sty.bold("Все пакеты загружены.")),
			"Пожалуйста, дождитесь начала обновления.",
		)
		printMissingUpgradeSpecsWarning(collectMissingUpgradeSpecs(rows), sty)

		return
	}

	if len(missingRequired) == 0 {
		fmt.Printf("%s %s\n",
			sty.yellow(sty.bold("Не загружены или отсутствуют файлы:")),
			sty.yellow(strings.Join(missingExceptional, ", ")),
		)
		fmt.Println(sty.red(sty.magenta("Эти ECU нужно обновить вручную!")))
		fmt.Println(sty.green(sty.bold("\nИсправление context.db можно запускать.")))
		printMissingUpgradeSpecsWarning(collectMissingUpgradeSpecs(rows), sty)

		return
	}

	if len(missingExceptional) > 0 {
		fmt.Printf("%s %s\n",
			sty.yellow(sty.bold("Не загружены или отсутствуют файлы (вручную):")),
			sty.yellow(strings.Join(missingExceptional, ", ")),
		)
	}

	fmt.Printf("%s %s\n",
		sty.red(sty.bold("Не загружены или отсутствуют файлы (обязательные):")),
		sty.red(strings.Join(missingRequired, ", ")),
	)
	fmt.Println("Пожалуйста, дождитесь загрузки всех обязательных пакетов.")
	fmt.Println(sty.red(sty.bold("\nИсправление context.db пока запускать нельзя.")))
}

func printFlashFailureStatus(rows []packageRow, sty textStyle) {
	missingExceptional, missingRequired := collectMissingECUs(rows)
	sort.Strings(missingExceptional)
	sort.Strings(missingRequired)

	fmt.Println(sty.bold(sty.cyan("=== Статус готовности ===")))

	if len(missingExceptional) == 0 && len(missingRequired) == 0 {
		fmt.Printf("%s\n%s\n",
			sty.green(sty.bold("Все пакеты загружены.")),
			"Исправление context.db не требуется.",
		)
		printMissingUpgradeSpecsWarning(collectMissingUpgradeSpecs(rows), sty)

		return
	}

	if len(missingExceptional) > 0 {
		fmt.Printf("%s %s\n",
			sty.bold("Ошибка прошивки в пакетах:"),
			sty.yellow(strings.Join(missingExceptional, ", ")),
		)
	} else {
		fmt.Printf("%s\n", sty.bold("Ошибка прошивки в пакетах:"))
	}

	if len(missingRequired) > 0 {
		fmt.Printf("%s %s\n",
			sty.red(sty.bold("Отсутствуют обязательные пакеты:")),
			sty.red(strings.Join(missingRequired, ", ")),
		)
	}

	if len(missingRequired) > 0 {
		fmt.Println(sty.red(sty.bold("\nОтсутствуют обязательные пакеты. Исправление context.db пока запускать нельзя.")))

		return
	}

	fmt.Println(sty.magenta(sty.bold("Эти ECU нужно обновить вручную!")))

	fmt.Println(sty.green(sty.bold("\nИсправление context.db можно запускать.")))
	printMissingUpgradeSpecsWarning(collectMissingUpgradeSpecs(rows), sty)
}

func collectMissingECUs(rows []packageRow) ([]string, []string) {
	exceptionalECUs := map[string]struct{}{
		iviMCUName: {},
		iviMPUName: {},
		tboxName:   {},
	}
	missingExceptional := make([]string, 0)
	missingRequired := make([]string, 0)

	for _, row := range rows {
		if isPackageReady(row) {
			continue
		}

		if _, ok := exceptionalECUs[row.ECU]; ok {
			missingExceptional = append(missingExceptional, row.ECU)

			continue
		}

		missingRequired = append(missingRequired, row.ECU)
	}

	return missingExceptional, missingRequired
}

func isPackageReady(row packageRow) bool {
	return (row.Status == packageStatusDownloaded || row.Status == packageStatusFlashed) &&
		row.FileExists
}

func collectMissingUpgradeSpecs(rows []packageRow) []string {
	missingUpgradeSpecs := make([]string, 0)

	for _, row := range rows {
		if !isPackageReady(row) || row.UpgradeSpecExists {
			continue
		}

		missingUpgradeSpecs = append(missingUpgradeSpecs, row.ECU)
	}

	sort.Strings(missingUpgradeSpecs)

	return missingUpgradeSpecs
}

func printMissingUpgradeSpecsWarning(missingUpgradeSpecs []string, sty textStyle) {
	if len(missingUpgradeSpecs) == 0 {
		return
	}

	fmt.Printf("%s %s\n",
		sty.yellow(sty.bold("\nПредупреждение: отсутствуют файлы спецификаций обновления (.otx):")),
		sty.yellow(strings.Join(missingUpgradeSpecs, ", ")),
	)
	fmt.Println(sty.yellow("Обновление OTA может не стартовать сразу."))
}

func styleExistsValue(exists bool, width int, sty textStyle) string {
	value := "нет"
	if exists {
		value = "есть"
	}

	padded := fmt.Sprintf("%-*s", width, value)
	if exists {
		return sty.green(padded)
	}

	return sty.red(padded)
}

func stylePackageStatus(status string, width int, sty textStyle) string {
	padded := fmt.Sprintf("%-*s", width, status)

	switch status {
	case packageStatusDownloaded, packageStatusFlashed:
		return sty.green(padded)
	case packageStatusDownloading:
		return sty.yellow(padded)
	case packageStatusPending:
		return sty.red(padded)
	default:
		return padded
	}
}

func colorizeByPercent(text string, percent int, sty textStyle) string {
	if percent >= percentHighThreshold {
		return sty.green(text)
	}

	if percent >= percentMediumThreshold {
		return sty.yellow(text)
	}

	return sty.red(text)
}

func formatBytes(bytes int64) string {
	if bytes < 0 {
		return fmt.Sprintf("%d B", bytes)
	}

	if bytes < byteUnitBase {
		return fmt.Sprintf("%d B", bytes)
	}

	units := []string{"B", "KB", "MB", "GB", "TB"}
	value := float64(bytes)
	unitIndex := 0

	for value >= byteUnitBase && unitIndex < len(units)-1 {
		value /= byteUnitBase
		unitIndex++
	}

	return fmt.Sprintf("%.2f %s", value, units[unitIndex])
}
