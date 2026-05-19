package app

import (
	"fmt"
	"io"
	"os"

	"github.com/pkg/sftp"
)

type sftpFileClient struct {
	client *sftp.Client
}

func (c *sftpFileClient) Close() error {
	return c.client.Close()
}

func (c *sftpFileClient) Exists(filePath string) bool {
	if filePath == "" {
		return false
	}

	_, err := c.client.Stat(filePath)

	return err == nil
}

func (c *sftpFileClient) ExistsAndNotEmpty(filePath string) bool {
	return fileExistsAndNotEmpty(c.client.Stat, filePath)
}

func (c *sftpFileClient) Download(remotePath, localPath string) error {
	src, err := c.client.Open(remotePath)
	if err != nil {
		return fmt.Errorf("ошибка открытия удаленного файла %s на T-Box: %w", remotePath, err)
	}
	defer src.Close()

	dst, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("ошибка создания локального файла %s: %w", localPath, err)
	}

	defer func() {
		_ = dst.Close()
	}()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("ошибка копирования T-Box->локально %s: %w", localPath, err)
	}

	if err := dst.Sync(); err != nil {
		return fmt.Errorf("ошибка синхронизации локального файла %s: %w", localPath, err)
	}

	return nil
}

func (c *sftpFileClient) Upload(localPath, remotePath string) error {
	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("ошибка открытия локального файла %s: %w", localPath, err)
	}
	defer src.Close()

	dst, err := c.client.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("ошибка открытия файла %s на T-Box для перезаписи: %w", remotePath, err)
	}

	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()

		return fmt.Errorf("ошибка копирования локально->T-Box %s: %w", remotePath, err)
	}

	if err := dst.Sync(); err != nil {
		_ = dst.Close()

		return fmt.Errorf("ошибка синхронизации файла %s на T-Box: %w", remotePath, err)
	}

	if err := dst.Close(); err != nil {
		return fmt.Errorf("ошибка закрытия файла %s на T-Box: %w", remotePath, err)
	}

	return nil
}

func (c *sftpFileClient) Remove(filePath string) error {
	if err := c.client.Remove(filePath); err != nil {
		return fmt.Errorf("ошибка удаления файла %s на T-Box: %w", filePath, err)
	}

	return nil
}
