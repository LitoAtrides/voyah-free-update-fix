package app

import (
	"database/sql"
	"fmt"
)

// GetOTATaskExpireTime returns expire_time from ota_task.taskData.
func GetOTATaskExpireTime(dbConn *sql.DB) (int64, bool, error) {
	row := dbConn.QueryRow(
		`SELECT json_extract(taskData, '$.expire_time')
		 FROM ota_task
		 WHERE type = 'task_info'`,
	)

	var expireTime sql.NullInt64
	if err := row.Scan(&expireTime); err != nil {
		return 0, false, fmt.Errorf("ошибка запроса expire_time из ota_task: %w", err)
	}

	if !expireTime.Valid {
		return 0, false, nil
	}

	return expireTime.Int64, true, nil
}

// SetOTATaskExpireTime updates expire_time inside ota_task.taskData.
func SetOTATaskExpireTime(dbConn *sql.DB, expireTime int64) error {
	result, err := dbConn.Exec(
		`UPDATE ota_task
		 SET taskData = json_set(taskData, '$.expire_time', ?)
		 WHERE type = 'task_info'
		   AND json_valid(taskData)`,
		expireTime,
	)
	if err != nil {
		return fmt.Errorf("ошибка обновления expire_time в ota_task: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("ошибка проверки обновленных строк для expire_time в ota_task: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("ошибка обновления expire_time в ota_task: не было обновлено ни одной строки")
	}

	return nil
}
