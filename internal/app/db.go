//nolint:noctx
package app

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

func connectToDB() (*sql.DB, error) {
	return connectToDBAt(localContextDBTmpFile)
}

func connectToDBAt(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия context.db: %w", err)
	}

	row := db.QueryRow("PRAGMA integrity_check;")

	var integrityResult string
	if err := row.Scan(&integrityResult); err != nil {
		db.Close()

		return nil, fmt.Errorf("ошибка проверки целостности базы данных: %w", err)
	}

	if integrityResult != "ok" {
		db.Close()

		return nil, fmt.Errorf("проверка целостности базы данных не прошла: %s", integrityResult)
	}

	return db, nil
}

func getOTATaskData(dbConn *sql.DB) (string, error) {
	var taskData string

	row := dbConn.QueryRow("SELECT taskData FROM ota_task WHERE type = 'task_info'")
	if err := row.Scan(&taskData); err != nil {
		return "", fmt.Errorf("ошибка запроса ota_task: %w", err)
	}

	return taskData, nil
}

func updateOTATaskData(dbConn *sql.DB, taskData string) error {
	result, err := dbConn.Exec(
		`UPDATE ota_task
		 SET taskData = ?
		 WHERE type = 'task_info'`,
		taskData,
	)
	if err != nil {
		return fmt.Errorf("ошибка обновления ota_task: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("ошибка обновления ota_task: ошибка получения количества измененных строк: %w", err)
	}

	if rowsAffected == 0 {
		return errors.New("ошибка обновления ota_task: ни одна строка не была обновлена")
	}

	return nil
}

//nolint:mnd
func removeUndownloadedPackagesInDB(dbConn *sql.DB, packageIDsToDelete []int) error {
	if len(packageIDsToDelete) == 0 {
		return nil
	}

	// Удаляем с конца массива к началу, чтобы индексы не съезжали.
	sort.Sort(sort.Reverse(sort.IntSlice(packageIDsToDelete)))

	removePaths := make([]string, 0, len(packageIDsToDelete)+2)
	removePaths = append(removePaths, "$.flash_state", "$.schedule_state")

	for _, idx := range packageIDsToDelete {
		if idx < 0 {
			return fmt.Errorf("ошибка удаления пакетов из ota_task: некорректный индекс пакета: %d", idx)
		}

		removePaths = append(removePaths, fmt.Sprintf("$.packages_info[%d]", idx))
	}

	placeholders := make([]string, 0, len(removePaths))
	args := make([]any, 0, len(removePaths)+2)

	for _, path := range removePaths {
		placeholders = append(placeholders, "?")
		args = append(args, path)
	}

	args = append(args,
		//nolint:misspell
		`{"download_type":0,"percents":90,"stage":"Retrive Packages"}`,
		`{"stage":"Download","state":"Process"}`,
	)

	//nolint:gosec
	query := fmt.Sprintf(`
UPDATE ota_task
SET taskData = json_set(
	json_remove(taskData, %s),
	'$.download_state', json(?),
	'$.overall_state', json(?)
)
WHERE "type" = 'task_info'
  AND json_valid(taskData)
  AND json_type(taskData, '$.packages_info') = 'array';
`, strings.Join(placeholders, ", "))

	result, err := dbConn.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("ошибка удаления пакетов из ota_task: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("ошибка удаления пакетов из ota_task: ошибка получения количества измененных строк: %w", err)
	}

	if rowsAffected == 0 {
		return errors.New("ошибка удаления пакетов из ota_task: ни одна строка не была обновлена")
	}

	return nil
}
