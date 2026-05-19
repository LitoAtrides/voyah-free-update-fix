package app

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	ipAddress = "172.16.104.20"
	sshPort   = "22"
	username  = "root"
	password  = "12345"
	rebootCmd = "reboot"
	backupDir = "backup"
)

type RuntimeConfig struct {
	IPAddress string
	SSHPort   string
	Username  string
	Password  string
	RebootCmd string
	BackupDir string
}

const (
	tboxDir                 = "/mnt/ota/data/fota"
	contextDBName           = "context.db"
	contextJSONName         = "context.json"
	localContextDBTmpFile   = "context.db.tmp"
	localContextJSONTmpFile = "context.json.tmp"
	rebootCmdEnvVar         = "REBOOT_TBOX_COMMAND"

	iviMCUName = "IVI_MCU"
	iviMPUName = "IVI_MPU"
	tboxName   = "T-BOX"

	colorReset = "\033[0m"
	colorRed   = "\033[31m"
	colorGreen = "\033[32m"
)

func Run() error {
	return RunWithConfig(DefaultRuntimeConfig())
}

func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		IPAddress: ipAddress,
		SSHPort:   sshPort,
		Username:  username,
		Password:  password,
		RebootCmd: rebootCmd,
		BackupDir: backupDir,
	}
}

func RunWithConfig(cfg RuntimeConfig) error {
	applyRuntimeConfig(cfg)

	sshClient, err := connectToTBox(net.JoinHostPort(ipAddress, sshPort), username, password)
	if err != nil {
		return fmt.Errorf("ошибка подключения к T-Box: %w", err)
	}
	defer sshClient.Close()

	remoteFS, err := newTBoxFileClient(sshClient)
	if err != nil {
		return fmt.Errorf("ошибка файлового транспорта: %w", err)
	}
	defer remoteFS.Close()

	taskData, acceptToStartFix, missingPackageIDs, err := analyzeContextDB(remoteFS)
	if err != nil {
		return fmt.Errorf("ошибка анализа context.db: %w", err)
	}

	if !acceptToStartFix {
		if err := runOTAScenarioMenu(sshClient, remoteFS, taskData); err != nil {
			return fmt.Errorf("ошибка сценария OTA: %w", err)
		}

		return nil
	}

	if !askConfirmation() {
		return nil
	}

	if err := fixContextDB(sshClient, remoteFS, missingPackageIDs); err != nil {
		return fmt.Errorf("ошибка исправления context.db: %w", err)
	}

	fmt.Printf(
		"%sФайл context.db успешно исправлен и загружен на T-Box!%s\n",
		colorGreen,
		colorReset,
	)

	return nil
}

func applyRuntimeConfig(cfg RuntimeConfig) {
	if cfgIPAddress := strings.TrimSpace(cfg.IPAddress); cfgIPAddress != "" {
		ipAddress = cfg.IPAddress
	}

	if cfgSSHPort := strings.TrimSpace(cfg.SSHPort); cfgSSHPort != "" {
		sshPort = cfg.SSHPort
	}

	if cfgUsername := strings.TrimSpace(cfg.Username); cfgUsername != "" {
		username = cfg.Username
	}

	if cfgPassword := strings.TrimSpace(cfg.Password); cfgPassword != "" {
		password = cfg.Password
	}

	if cfgRebootCmd := strings.TrimSpace(cfg.RebootCmd); cfgRebootCmd != "" {
		rebootCmd = cfg.RebootCmd
	}

	if cfgBackupDir := strings.TrimSpace(cfg.BackupDir); cfgBackupDir != "" {
		backupDir = cfg.BackupDir
	}
}

func analyzeContextDB(client tboxFileClient) (otaTaskData, bool, []int, error) {
	err := downloadContextDB(client)
	if err != nil {
		return otaTaskData{}, false, nil, fmt.Errorf("ошибка загрузки context.db: %w", err)
	}

	defer os.Remove(localContextDBTmpFile)

	dbConn, err := connectToDB()
	if err != nil {
		return otaTaskData{}, false, nil, err
	}
	defer dbConn.Close()

	taskDataJSON, err := getOTATaskData(dbConn)
	if err != nil {
		return otaTaskData{}, false, nil, err
	}

	taskData, err := parseOTATaskData(taskDataJSON)
	if err != nil {
		return otaTaskData{}, false, nil, err
	}

	rows, stats := buildPackageRowsAndStats(taskData.PackagesInfo, client)

	printTaskDataInfo(taskData, rows, stats)

	fixRequired, missingPackageIDs := checkFixContextDB(rows)
	if !fixRequired {
		return taskData, false, nil, nil
	}

	if !isFixStateAllowed(taskData) {
		fmt.Println()
		fmt.Printf(
			"%sИсправление context.db можно запускать только в одном из состояний:%s\n",
			colorRed,
			colorReset,
		)
		fmt.Println("  - download_state.stage='Retrive Packages' и пакеты IVI_MCU, IVI_MPU или T-Box еще не загружены")
		fmt.Println("  - download_state.stage='Complete' и ошибка прошивки IVI_MCU, IVI_MPU или T-Box")
		fmt.Println("  - download_state.stage='Complete', overall_state.stage='Terminate', overall_state.state='Idle'")
		fmt.Println("      и отсутствуют файлы пакетов IVI_MCU, IVI_MPU или T-Box")

		return taskData, false, nil, nil
	}

	return taskData, true, missingPackageIDs, nil
}

//nolint:cyclop
func fixContextDB(sshClient *ssh.Client, client tboxFileClient, missingPackageIDs []int) error {
	fmt.Println()
	fmt.Println("Начинаю исправление context.db...")

	err := downloadContextDB(client)
	if err != nil {
		return fmt.Errorf("ошибка загрузки context.db: %w", err)
	}

	defer os.Remove(localContextDBTmpFile)

	existContextJSON := true

	err = downloadContextJSON(client)
	if err != nil {
		if !errors.Is(err, errContextJSONNotFound) {
			return fmt.Errorf("ошибка загрузки context.json: %w", err)
		}

		existContextJSON = false
	} else {
		defer os.Remove(localContextJSONTmpFile)
	}

	if _, err := backup(existContextJSON); err != nil {
		return fmt.Errorf("ошибка создания backup контекста: %w", err)
	}

	dbConn, err := connectToDB()
	if err != nil {
		return err
	}

	if err := removeUndownloadedPackagesInDB(dbConn, missingPackageIDs); err != nil {
		return err
	}

	dbConn.Close()

	if existContextJSON {
		if err := deleteContextJSONOnTBox(client); err != nil {
			return fmt.Errorf("ошибка удаления context.json на T-Box: %w", err)
		}

		fmt.Println("Context.json удален с T-Box.")
	}

	if err := uploadContextDB(client); err != nil {
		return fmt.Errorf("ошибка загрузки исправленного context.db: %w", err)
	}

	fmt.Println("Исправленный context.db загружен на T-Box.")

	if err := rebootTBox(sshClient); err != nil {
		return fmt.Errorf("ошибка перезагрузки T-Box: %w", err)
	}

	fmt.Println("T-Box перезагружается...")

	return nil
}

func runOTAScenarioMenu(sshClient *ssh.Client, client tboxFileClient, taskData otaTaskData) error {
	for {
		fmt.Println()
		printOTAScenarioSnapshot(taskData)
		fmt.Println("Сценарий OTA:")
		fmt.Println("1. Сделать бэкап")
		fmt.Println("2. Перезапустить OTA")
		fmt.Println("3. Обновить expire_time")
		fmt.Println("4. Перезагрузить T-Box")
		fmt.Println("5. Показать готовность OTA")
		fmt.Println("6. Выйти")

		choice, err := promptLine(bufio.NewReader(os.Stdin), os.Stdout, "Выберите действие", "1")
		if err != nil {
			return err
		}

		switch strings.TrimSpace(choice) {
		case "1":
			backupPath, err := backupCurrentContextForExpireTime(client)
			if err != nil {
				return err
			}
			fmt.Printf("Имя архива: %s\n", filepath.Base(backupPath))
			fmt.Printf("Backup текущего состояния создан: %s\n", backupPath)
		case "2":
			var err error
			sshClient, client, err = restartOTAFlow(sshClient, client, &taskData)
			if err != nil {
				return err
			}
		case "3":
			var err error
			sshClient, client, err = updateExpireTimeFlow(sshClient, client, &taskData)
			if err != nil {
				return err
			}
		case "4":
			var err error
			sshClient, client, err = rebootTBoxAndWait(sshClient, client)
			if err != nil {
				return err
			}
		case "5":
		case "6":
			return nil
		default:
			fmt.Println("Неизвестная команда.")
		}
	}
}

func printOTAScenarioSnapshot(taskData otaTaskData) {
	expireTime := "нет"
	expiredText := "неизвестно"
	now := time.Now().In(time.Local)

	if taskData.ExpireTime != nil && *taskData.ExpireTime > 0 {
		expireUnix := int64(*taskData.ExpireTime)
		expireTime = time.Unix(expireUnix, 0).Format("02.01.2006 15:04:05")
		if isExpireTimeExpired(expireUnix, now) {
			expiredText = "просрочен"
		} else {
			expiredText = "актуален"
		}
	}

	fmt.Println("=== Сценарий OTA ===")
	fmt.Printf("expire_time: %s (%s)\n", expireTime, expiredText)
	fmt.Printf("overall_state: stage=%s state=%s\n", taskData.OverallState.Stage, taskData.OverallState.State)
	fmt.Printf("download_state: stage=%s progress=%d%%\n", taskData.DownloadState.Stage, taskData.DownloadState.Percents)
}

func isExpireTimeExpired(expireTime int64, now time.Time) bool {
	return expireTime > 0 && time.Unix(expireTime, 0).Before(now)
}

func restartOTAFlow(sshClient *ssh.Client, client tboxFileClient, taskData *otaTaskData) (*ssh.Client, tboxFileClient, error) {
	backupPath, err := backupCurrentContextForExpireTime(client)
	if err != nil {
		return sshClient, client, err
	}
	fmt.Printf("Имя архива: %s\n", filepath.Base(backupPath))
	fmt.Printf("Backup текущего состояния создан: %s\n", backupPath)

	archivePath, err := selectBackupArchive()
	if err != nil {
		return sshClient, client, err
	}

	fmt.Printf("Восстанавливаю OTA из архива: %s\n", filepath.Base(archivePath))

	restoredTaskData, err := restoreOTATaskDataFromArchive(client, archivePath)
	if err != nil {
		return sshClient, client, err
	}

	*taskData = restoredTaskData

	fmt.Println("OTA восстановлена из бэкапа.")

	return updateExpireTimeFlowWithBackup(sshClient, client, taskData, false)
}

func updateExpireTimeFlow(sshClient *ssh.Client, client tboxFileClient, taskData *otaTaskData) (*ssh.Client, tboxFileClient, error) {
	return updateExpireTimeFlowWithBackup(sshClient, client, taskData, true)
}

func updateExpireTimeFlowWithBackup(sshClient *ssh.Client, client tboxFileClient, taskData *otaTaskData, createBackup bool) (*ssh.Client, tboxFileClient, error) {
	if createBackup {
		backupPath, err := backupCurrentContextForExpireTime(client)
		if err != nil {
			return sshClient, client, err
		}
		fmt.Printf("Имя архива: %s\n", filepath.Base(backupPath))
		fmt.Printf("Backup текущего состояния создан: %s\n", backupPath)
	}

	now := time.Now().In(time.Local)

	newExpireTime, err := promptExpireTimeForUpdate(os.Stdin, os.Stdout, now)
	if err != nil {
		return sshClient, client, err
	}

	fmt.Println()
	fmt.Printf("Выбрано: %s\n", time.Unix(newExpireTime, 0).Format("02.01.2006 15:04:05"))
	fmt.Println("Можно обновлять expire_time.")

	if !askConfirmationForExpireTimeUpdate(os.Stdin, os.Stdout) {
		fmt.Println("Неверное подтверждение. Обновление expire_time не запущено.")
		return sshClient, client, nil
	}

	if err := updateExpireTimeOnTBox(client, newExpireTime); err != nil {
		return sshClient, client, err
	}

	updated := otaExpireTime(newExpireTime)
	taskData.ExpireTime = &updated

	fmt.Println("expire_time обновлен на T-Box.")

	if askRebootAfterExpireTimeUpdate(os.Stdin, os.Stdout) {
		var err error
		sshClient, client, err = rebootTBoxAndWait(sshClient, client)
		if err != nil {
			return sshClient, client, err
		}
	} else {
		fmt.Println("T-Box не перезагружается.")
	}

	return sshClient, client, nil
}

func selectBackupArchive() (string, error) {
	archives, err := listBackupArchives()
	if err != nil {
		return "", err
	}

	if len(archives) == 0 {
		return "", fmt.Errorf("в каталоге %s не найдено backup-архивов", backupDir)
	}

	fmt.Println("Выберите backup-архив для восстановления OTA:")
	for i, archive := range archives {
		fmt.Printf("%d. %s\n", i+1, filepath.Base(archive))
	}

	choice, err := promptLine(bufio.NewReader(os.Stdin), os.Stdout, "Выберите архив", "1")
	if err != nil {
		return "", err
	}

	idx, err := strconv.Atoi(strings.TrimSpace(choice))
	if err != nil {
		return "", fmt.Errorf("некорректный выбор архива: %q", choice)
	}

	if idx < 1 || idx > len(archives) {
		return "", fmt.Errorf("выбор архива вне диапазона: %d", idx)
	}

	return archives[idx-1], nil
}

func listBackupArchives() ([]string, error) {
	pattern := filepath.Join(backupDir, "context.backup.*.zip")
	archives, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("ошибка поиска backup-архивов: %w", err)
	}

	sort.Slice(archives, func(i, j int) bool {
		leftInfo, leftErr := os.Stat(archives[i])
		rightInfo, rightErr := os.Stat(archives[j])

		if leftErr != nil || rightErr != nil {
			return archives[i] > archives[j]
		}

		return leftInfo.ModTime().After(rightInfo.ModTime())
	})

	return archives, nil
}

func restoreOTATaskDataFromArchive(client tboxFileClient, archivePath string) (otaTaskData, error) {
	tmpDir, err := os.MkdirTemp("", "ota-backup-restore-*")
	if err != nil {
		return otaTaskData{}, fmt.Errorf("ошибка создания временного каталога: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := unzipArchive(archivePath, tmpDir); err != nil {
		return otaTaskData{}, err
	}

	backupDBPath := filepath.Join(tmpDir, contextDBName)
	if _, err := os.Stat(backupDBPath); err != nil {
		return otaTaskData{}, fmt.Errorf("в backup-архиве нет %s: %w", contextDBName, err)
	}

	backupDB, err := connectToDBAt(backupDBPath)
	if err != nil {
		return otaTaskData{}, err
	}
	defer backupDB.Close()

	taskDataJSON, err := getOTATaskData(backupDB)
	if err != nil {
		return otaTaskData{}, err
	}

	if err := downloadContextDB(client); err != nil {
		return otaTaskData{}, fmt.Errorf("ошибка загрузки context.db для восстановления OTA: %w", err)
	}
	defer os.Remove(localContextDBTmpFile)

	currentDB, err := connectToDB()
	if err != nil {
		return otaTaskData{}, err
	}
	defer currentDB.Close()

	currentTaskDataJSON, err := getOTATaskData(currentDB)
	if err != nil {
		return otaTaskData{}, err
	}

	mergedTaskDataJSON, restoredTaskData, err := buildRestartOTATaskData(currentTaskDataJSON, taskDataJSON)
	if err != nil {
		return otaTaskData{}, err
	}

	if err := updateOTATaskData(currentDB, mergedTaskDataJSON); err != nil {
		return otaTaskData{}, err
	}

	if err := uploadContextDB(client); err != nil {
		return otaTaskData{}, fmt.Errorf("ошибка загрузки восстановленного context.db: %w", err)
	}

	return restoredTaskData, nil
}

func buildRestartOTATaskData(currentTaskDataJSON, backupTaskDataJSON string) (string, otaTaskData, error) {
	var currentTaskData map[string]any
	if err := json.Unmarshal([]byte(currentTaskDataJSON), &currentTaskData); err != nil {
		return "", otaTaskData{}, fmt.Errorf("некорректный JSON текущей ota_task.taskData: %w", err)
	}

	var backupTaskData map[string]any
	if err := json.Unmarshal([]byte(backupTaskDataJSON), &backupTaskData); err != nil {
		return "", otaTaskData{}, fmt.Errorf("некорректный JSON backup ota_task.taskData: %w", err)
	}

	copyIfExists := func(key string) {
		if value, ok := backupTaskData[key]; ok {
			currentTaskData[key] = value
		}
	}

	copyIfExists("target_baseline_version")
	copyIfExists("predict_upgrade_duration")
	copyIfExists("preconditions_config")
	copyIfExists("schedule_state")
	copyIfExists("expire_time")

	if packages, ok := backupTaskData["packages_info"].([]any); ok {
		currentTaskData["packages_info"] = resetPackagesForRestart(packages)
	}

	currentTaskData["overall_state"] = map[string]any{
		"stage": "Download",
		"state": "Process",
	}
	currentTaskData["download_state"] = map[string]any{
		"download_type": 0,
		"percents":      0,
		"stage":         "Retrive Packages",
	}
	delete(currentTaskData, "flash_state")

	mergedTaskDataJSONBytes, err := json.Marshal(currentTaskData)
	if err != nil {
		return "", otaTaskData{}, fmt.Errorf("ошибка сериализации ota_task.taskData: %w", err)
	}

	restoredTaskData, err := parseOTATaskData(string(mergedTaskDataJSONBytes))
	if err != nil {
		return "", otaTaskData{}, err
	}

	return string(mergedTaskDataJSONBytes), restoredTaskData, nil
}

func resetPackagesForRestart(packages []any) []any {
	resetPackages := make([]any, 0, len(packages))

	for _, item := range packages {
		pkg, ok := item.(map[string]any)
		if !ok {
			resetPackages = append(resetPackages, item)
			continue
		}

		pkg["downloaded_size"] = float64(0)
		pkg["flash_finish"] = false
		pkg["start_flash_time"] = float64(0)
		pkg["end_flash_time"] = float64(0)
		pkg["start_rollback_time"] = float64(0)
		pkg["end_rollback_time"] = float64(0)
		resetPackages = append(resetPackages, pkg)
	}

	return resetPackages
}

func unzipArchive(archivePath, targetDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("ошибка открытия backup-архива %s: %w", archivePath, err)
	}
	defer zr.Close()

	for _, file := range zr.File {
		targetPath := filepath.Join(targetDir, file.Name)

		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return fmt.Errorf("ошибка создания каталога %s: %w", targetPath, err)
			}

			continue
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("ошибка создания каталога %s: %w", filepath.Dir(targetPath), err)
		}

		src, err := file.Open()
		if err != nil {
			return fmt.Errorf("ошибка открытия файла %s в backup-архиве: %w", file.Name, err)
		}

		dst, err := os.Create(targetPath)
		if err != nil {
			_ = src.Close()

			return fmt.Errorf("ошибка создания файла %s: %w", targetPath, err)
		}

		if _, err := io.Copy(dst, src); err != nil {
			_ = src.Close()
			_ = dst.Close()

			return fmt.Errorf("ошибка распаковки файла %s: %w", file.Name, err)
		}

		if err := dst.Close(); err != nil {
			_ = src.Close()

			return fmt.Errorf("ошибка закрытия файла %s: %w", targetPath, err)
		}

		if err := src.Close(); err != nil {
			return fmt.Errorf("ошибка закрытия файла %s в backup-архиве: %w", file.Name, err)
		}
	}

	return nil
}

func backupCurrentContextForExpireTime(client tboxFileClient) (string, error) {
	if err := downloadContextDB(client); err != nil {
		return "", fmt.Errorf("ошибка загрузки context.db перед обновлением expire_time: %w", err)
	}
	defer os.Remove(localContextDBTmpFile)

	existContextJSON := true
	if err := downloadContextJSON(client); err != nil {
		if !errors.Is(err, errContextJSONNotFound) {
			return "", fmt.Errorf("ошибка загрузки context.json перед обновлением expire_time: %w", err)
		}

		existContextJSON = false
	} else {
		defer os.Remove(localContextJSONTmpFile)
	}

	backupPath, err := backup(existContextJSON)
	if err != nil {
		return "", fmt.Errorf("ошибка создания backup перед обновлением expire_time: %w", err)
	}

	return backupPath, nil
}

func rebootTBoxAndWait(sshClient *ssh.Client, client tboxFileClient) (*ssh.Client, tboxFileClient, error) {
	if err := rebootTBox(sshClient); err != nil {
		return sshClient, client, fmt.Errorf("ошибка перезагрузки T-Box: %w", err)
	}

	fmt.Println("T-Box перезагружается...")

	newSSHClient, newClient, err := waitForTBoxReconnect()
	if err != nil {
		return sshClient, client, err
	}

	_ = client.Close()
	_ = sshClient.Close()

	fmt.Println("T-Box снова доступен.")

	return newSSHClient, newClient, nil
}

func waitForTBoxReconnect() (*ssh.Client, tboxFileClient, error) {
	addr := net.JoinHostPort(ipAddress, sshPort)
	deadline := time.Now().Add(5 * time.Minute)
	delay := 10 * time.Second
	maxAttempts := int(time.Until(deadline)/delay) + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	fmt.Println("Жду повторного подключения к T-Box...")

	attempt := 1
	for {
		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf("T-Box не вернулся в сеть за 5 минут")
		}

		fmt.Printf("Попытка %d/%d: пробую подключиться к T-Box...\n", attempt, maxAttempts)
		sshClient, err := connectToTBox(addr, username, password)
		if err == nil {
			fmt.Println("Подключение к T-Box восстановлено.")
			client, err := newTBoxFileClient(sshClient)
			if err == nil {
				return sshClient, client, nil
			}

			_ = sshClient.Close()
			fmt.Printf("не удалось открыть файловый транспорт после перезагрузки: %v\n", err)
		}

		fmt.Printf("Повторю попытку через %s.\n", delay)
		time.Sleep(delay)
		attempt++
	}
}

func askConfirmationForExpireTimeUpdate(in io.Reader, out io.Writer) bool {
	confirmation, err := promptLine(bufio.NewReader(in), out, "Подтвердите обновление expire_time. Введите 'start'", "start")
	if err != nil {
		return false
	}

	return strings.EqualFold(strings.TrimSpace(confirmation), "start")
}

func askRebootAfterExpireTimeUpdate(in io.Reader, out io.Writer) bool {
	confirmation, err := promptLine(bufio.NewReader(in), out, "Перезагрузить T-Box. Введите 'start'", "start")
	if err != nil {
		return false
	}

	return strings.EqualFold(strings.TrimSpace(confirmation), "start")
}

func promptExpireTimeForUpdate(in io.Reader, out io.Writer, now time.Time) (int64, error) {
	reader := bufio.NewReader(in)
	defaultValue := now.Add(15 * time.Minute).Format("02.01.2006 15:04")

	value, err := promptLine(reader, out, "Дата и время expire_time (ДД.ММ.ГГГГ ЧЧ:ММ)", defaultValue)
	if err != nil {
		return 0, err
	}

	return parseExpireDateTime(value, time.Local)
}

func parseExpireDateTime(value string, loc *time.Location) (int64, error) {
	parts := strings.Fields(value)
	if len(parts) != 2 {
		return 0, fmt.Errorf("некорректная дата и время %q: ожидается формат ДД.ММ.ГГГГ ЧЧ:ММ", value)
	}

	dateParts := strings.Split(parts[0], ".")
	if len(dateParts) != 3 {
		return 0, fmt.Errorf("некорректная дата %q: ожидается формат ДД.ММ.ГГГГ", parts[0])
	}

	day, err := strconv.Atoi(dateParts[0])
	if err != nil {
		return 0, fmt.Errorf("некорректный день в дате %q: %w", parts[0], err)
	}

	month, err := strconv.Atoi(dateParts[1])
	if err != nil {
		return 0, fmt.Errorf("некорректный месяц в дате %q: %w", parts[0], err)
	}

	year, err := strconv.Atoi(dateParts[2])
	if err != nil {
		return 0, fmt.Errorf("некорректный год в дате %q: %w", parts[0], err)
	}

	timeParts := strings.Split(parts[1], ":")
	if len(timeParts) != 2 {
		return 0, fmt.Errorf("некорректное время %q: ожидается формат ЧЧ:ММ", parts[1])
	}

	hour, err := strconv.Atoi(timeParts[0])
	if err != nil {
		return 0, fmt.Errorf("некорректный час во времени %q: %w", parts[1], err)
	}

	minute, err := strconv.Atoi(timeParts[1])
	if err != nil {
		return 0, fmt.Errorf("некорректная минута во времени %q: %w", parts[1], err)
	}

	if month < 1 || month > 12 {
		return 0, fmt.Errorf("некорректный месяц в дате %q", parts[0])
	}

	if day < 1 || day > 31 {
		return 0, fmt.Errorf("некорректный день в дате %q", parts[0])
	}

	if hour < 0 || hour > 23 {
		return 0, fmt.Errorf("некорректный час во времени %q", parts[1])
	}

	if minute < 0 || minute > 59 {
		return 0, fmt.Errorf("некорректная минута во времени %q", parts[1])
	}

	expireAt := time.Date(year, time.Month(month), day, hour, minute, 0, 0, loc)
	if expireAt.Year() != year || int(expireAt.Month()) != month || expireAt.Day() != day ||
		expireAt.Hour() != hour || expireAt.Minute() != minute {
		return 0, fmt.Errorf("не удалось собрать значение expire_time из %q", value)
	}

	return expireAt.Unix(), nil
}

func promptLine(reader *bufio.Reader, out io.Writer, label, defaultValue string) (string, error) {
	fmt.Fprintf(out, "%s [%s]: ", label, defaultValue)

	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("ошибка чтения ввода для %s: %w", label, err)
	}

	value := strings.TrimSpace(line)
	if value == "" {
		return defaultValue, nil
	}

	return value, nil
}

func updateExpireTimeOnTBox(client tboxFileClient, expireTime int64) error {
	if err := downloadContextDB(client); err != nil {
		return fmt.Errorf("ошибка загрузки context.db: %w", err)
	}
	defer os.Remove(localContextDBTmpFile)

	dbConn, err := connectToDB()
	if err != nil {
		return err
	}
	defer dbConn.Close()

	if err := SetOTATaskExpireTime(dbConn, expireTime); err != nil {
		return err
	}

	if err := uploadContextDB(client); err != nil {
		return fmt.Errorf("ошибка загрузки обновленного context.db: %w", err)
	}

	return nil
}

func askConfirmation() bool {
	fmt.Print("\nВы уверены, что хотите запустить исправление context.db на T-Box? Введите 'start' для подтверждения: ")

	reader := bufio.NewReader(os.Stdin)

	confirmation, err := reader.ReadString('\n')
	if err != nil {
		fmt.Printf("не удалось прочитать подтверждение: %v\n", err)

		return false
	}

	if strings.TrimSpace(confirmation) != "start" {
		fmt.Printf("%sНеверное подтверждение. Исправление не было запущено.%s\n", colorRed, colorReset)

		return false
	}

	return true
}
