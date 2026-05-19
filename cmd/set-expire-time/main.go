package main

import (
	"bufio"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"voyah-free-update-fix/internal/app"

	_ "modernc.org/sqlite"
)

func main() {
	var dbPath string
	var backup bool

	flag.StringVar(&dbPath, "db", "", "path to context.db")
	flag.BoolVar(&backup, "backup", true, "create a local backup copy before writing")
	flag.Parse()

	if dbPath == "" {
		fmt.Fprintln(os.Stderr, "не указан обязательный флаг: --db")
		waitForExitOnWindows()
		os.Exit(1)
	}

	if err := run(dbPath, backup); err != nil {
		fmt.Fprintln(os.Stderr, err)
		waitForExitOnWindows()
		os.Exit(1)
	}

	waitForExitOnWindows()
}

func run(dbPath string, createBackup bool) error {
	expireTime, err := promptExpireTime(os.Stdin, os.Stdout, time.Now().In(time.Local))
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("Выбранное значение expire_time: %s\n", time.Unix(expireTime, 0).Format("02.01.2006 15:04:05"))
	fmt.Println("Обновление expire_time можно запускать.")

	if !askConfirmation(os.Stdin, os.Stdout) {
		fmt.Println("Неверное подтверждение. Обновление expire_time не было запущено.")
		return nil
	}

	if createBackup {
		if err := backupFile(dbPath); err != nil {
			return err
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("ошибка открытия %s: %w", dbPath, err)
	}
	defer db.Close()

	if err := checkIntegrity(db); err != nil {
		return err
	}

	currentExpireTime, exists, err := app.GetOTATaskExpireTime(db)
	if err != nil {
		return err
	}

	if err := app.SetOTATaskExpireTime(db, expireTime); err != nil {
		return err
	}

	fmt.Printf("expire_time обновлен в %s\n", dbPath)
	if exists {
		fmt.Printf("предыдущее: %d (%s)\n", currentExpireTime, time.Unix(currentExpireTime, 0).Format("02.01.2006 15:04:05"))
	}
	fmt.Printf("текущее:   %d (%s)\n", expireTime, time.Unix(expireTime, 0).Format("02.01.2006 15:04:05"))

	return nil
}

func askConfirmation(in io.Reader, out io.Writer) bool {
	reader := bufio.NewReader(in)

	fmt.Fprint(out, "Вы уверены, что хотите запустить обновление expire_time в context.db? Введите 'start' для подтверждения: ")

	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}

	return strings.TrimSpace(line) == "start"
}

func promptExpireTime(in io.Reader, out io.Writer, now time.Time) (int64, error) {
	reader := bufio.NewReader(in)

	defaultValue := now.Add(15 * time.Minute).Format("02.01.2006 15:04")
	value, err := promptLine(reader, out, "Введите дату и время expiry (ДД.ММ.ГГГГ ЧЧ:ММ)", defaultValue)
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

func backupFile(dbPath string) error {
	src, err := os.Open(dbPath)
	if err != nil {
		return fmt.Errorf("ошибка открытия исходной БД %s: %w", dbPath, err)
	}
	defer src.Close()

	stat, err := src.Stat()
	if err != nil {
		return fmt.Errorf("ошибка чтения метаданных исходной БД %s: %w", dbPath, err)
	}

	backupPath := dbPath + ".bak." + time.Now().Format("20060102_150405")
	dst, err := os.Create(backupPath)
	if err != nil {
		return fmt.Errorf("ошибка создания backup %s: %w", backupPath, err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("ошибка записи backup %s: %w", backupPath, err)
	}

	if err := dst.Chmod(stat.Mode()); err != nil {
		return fmt.Errorf("ошибка установки прав доступа для backup %s: %w", backupPath, err)
	}

	fmt.Printf("backup создан: %s\n", backupPath)

	return nil
}

func checkIntegrity(db *sql.DB) error {
	row := db.QueryRow("PRAGMA integrity_check;")

	var integrityResult string
	if err := row.Scan(&integrityResult); err != nil {
		return fmt.Errorf("ошибка проверки целостности базы данных: %w", err)
	}

	if integrityResult != "ok" {
		return fmt.Errorf("проверка целостности базы данных не прошла: %s", integrityResult)
	}

	return nil
}

func waitForExitOnWindows() {
	if runtime.GOOS != "windows" {
		return
	}

	stdinInfo, err := os.Stdin.Stat()
	if err != nil {
		return
	}

	if (stdinInfo.Mode() & os.ModeCharDevice) == 0 {
		return
	}

	fmt.Print("Нажмите любую клавишу для выхода...")

	reader := bufio.NewReader(os.Stdin)
	_, _ = reader.ReadByte()
}
