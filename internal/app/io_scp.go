package app

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

type scpFileClient struct {
	sshClient *ssh.Client
}

func (c *scpFileClient) Close() error {
	return nil
}

func (c *scpFileClient) ping() error {
	_, err := c.runCommand("command -v scp >/dev/null 2>&1")
	if err != nil {
		return fmt.Errorf("scp недоступен на T-Box: %w", err)
	}

	return nil
}

func (c *scpFileClient) Exists(filePath string) bool {
	if filePath == "" {
		return false
	}

	_, err := c.runCommand("test -e " + shellQuote(filePath))

	return err == nil
}

func (c *scpFileClient) ExistsAndNotEmpty(filePath string) bool {
	if filePath == "" {
		return false
	}

	_, err := c.runCommand("test -s " + shellQuote(filePath))

	return err == nil
}

func (c *scpFileClient) Download(remotePath, localPath string) error {
	dst, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("ошибка создания локального файла %s: %w", localPath, err)
	}
	defer dst.Close()

	if err := c.downloadWithSCP(remotePath, dst); err != nil {
		return err
	}

	if err := dst.Sync(); err != nil {
		return fmt.Errorf("ошибка синхронизации локального файла %s: %w", localPath, err)
	}

	return nil
}

func (c *scpFileClient) Upload(localPath, remotePath string) error {
	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("ошибка открытия локального файла %s: %w", localPath, err)
	}
	defer src.Close()

	srcInfo, err := src.Stat()
	if err != nil {
		return fmt.Errorf("ошибка получения статистики локального файла %s: %w", localPath, err)
	}

	if err := c.uploadWithSCP(remotePath, src, srcInfo.Size()); err != nil {
		return err
	}

	return nil
}

func (c *scpFileClient) Remove(filePath string) error {
	if _, err := c.runCommand("rm -f " + shellQuote(filePath)); err != nil {
		return fmt.Errorf("ошибка удаления файла %s на T-Box: %w", filePath, err)
	}

	return nil
}

//nolint:unparam
func (c *scpFileClient) runCommand(cmd string) ([]byte, error) {
	session, err := c.sshClient.NewSession()
	if err != nil {
		return nil, fmt.Errorf("ошибка создания SSH-сессии: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(cmd)
	if err != nil {
		trimmedOutput := strings.TrimSpace(string(output))
		if trimmedOutput == "" {
			return nil, err
		}

		return nil, fmt.Errorf("%w: %s", err, trimmedOutput)
	}

	return output, nil
}

//nolint:cyclop,funlen
func (c *scpFileClient) downloadWithSCP(remotePath string, dst io.Writer) error {
	session, err := c.sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("ошибка создания SSH-сессии: %w", err)
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("ошибка создания stdin pipe для scp: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ошибка создания stdout pipe для scp: %w", err)
	}

	var stderr bytes.Buffer

	session.Stderr = &stderr

	if err := session.Start("scp -f " + shellQuote(remotePath)); err != nil {
		return fmt.Errorf("ошибка запуска команды scp для загрузки: %w", err)
	}

	reader := bufio.NewReader(stdout)

	if _, err := stdin.Write([]byte{0}); err != nil {
		return fmt.Errorf("ошибка отправки начального scp ack: %w", err)
	}

	fileSize, err := readSCPHeader(reader)
	if err != nil {
		return fmt.Errorf("ошибка чтения scp-заголовка: %w", err)
	}

	if _, err := stdin.Write([]byte{0}); err != nil {
		return fmt.Errorf("ошибка подтверждения scp-заголовка: %w", err)
	}

	if _, err := io.CopyN(dst, reader, fileSize); err != nil {
		return fmt.Errorf("ошибка чтения данных файла через scp: %w", err)
	}

	if err := readSCPAck(reader); err != nil {
		return fmt.Errorf("ошибка ожидания финального scp ack: %w", err)
	}

	if _, err := stdin.Write([]byte{0}); err != nil {
		return fmt.Errorf("ошибка отправки финального scp ack: %w", err)
	}

	if err := stdin.Close(); err != nil {
		return fmt.Errorf("ошибка закрытия scp stdin: %w", err)
	}

	if err := session.Wait(); err != nil {
		stderrText := strings.TrimSpace(stderr.String())
		if stderrText != "" {
			return fmt.Errorf("загрузка через scp не удалась: %w: %s", err, stderrText)
		}

		return fmt.Errorf("загрузка через scp не удалась: %w", err)
	}

	return nil
}

//nolint:cyclop,funlen
func (c *scpFileClient) uploadWithSCP(remotePath string, src io.Reader, size int64) error {
	session, err := c.sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("ошибка создания SSH-сессии: %w", err)
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("ошибка создания stdin pipe для scp: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ошибка создания stdout pipe для scp: %w", err)
	}

	var stderr bytes.Buffer

	session.Stderr = &stderr

	if err := session.Start("scp -t " + shellQuote(remotePath)); err != nil {
		return fmt.Errorf("ошибка запуска команды scp для загрузки: %w", err)
	}

	reader := bufio.NewReader(stdout)
	if err := readSCPAck(reader); err != nil {
		return fmt.Errorf("удаленный scp не готов принимать файл: %w", err)
	}

	if _, err := fmt.Fprintf(stdin, "C0644 %d %s\n", size, path.Base(remotePath)); err != nil {
		return fmt.Errorf("ошибка отправки scp-заголовка файла: %w", err)
	}

	if err := readSCPAck(reader); err != nil {
		return fmt.Errorf("удаленный scp отклонил заголовок файла: %w", err)
	}

	if _, err := io.Copy(stdin, src); err != nil {
		return fmt.Errorf("ошибка отправки данных файла через scp: %w", err)
	}

	if _, err := stdin.Write([]byte{0}); err != nil {
		return fmt.Errorf("ошибка отправки маркера конца файла через scp: %w", err)
	}

	if err := readSCPAck(reader); err != nil {
		return fmt.Errorf("удаленный scp отклонил загруженные данные: %w", err)
	}

	if err := stdin.Close(); err != nil {
		return fmt.Errorf("ошибка закрытия scp stdin: %w", err)
	}

	if err := session.Wait(); err != nil {
		stderrText := strings.TrimSpace(stderr.String())
		if stderrText != "" {
			return fmt.Errorf("загрузка через scp не удалась: %w: %s", err, stderrText)
		}

		return fmt.Errorf("загрузка через scp не удалась: %w", err)
	}

	return nil
}

//nolint:cyclop,mnd
func readSCPHeader(reader *bufio.Reader) (int64, error) {
	firstByte, err := reader.ReadByte()
	if err != nil {
		return 0, err
	}

	switch firstByte {
	case 0x01, 0x02:
		message, readErr := reader.ReadString('\n')
		if readErr != nil {
			return 0, readErr
		}

		return 0, errors.New(strings.TrimSpace(message))
	case 'C':
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			return 0, readErr
		}

		parts := strings.Fields(strings.TrimSpace(line))
		if len(parts) != 3 {
			return 0, fmt.Errorf("неожиданный формат scp-заголовка: %q", line)
		}

		if !isValidSCPFileMode(parts[0]) {
			return 0, fmt.Errorf("некорректный режим файла scp в заголовке %q", line)
		}

		size, parseErr := strconv.ParseInt(parts[1], 10, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("некорректный размер файла scp в заголовке %q: %w", line, parseErr)
		}

		if size < 0 {
			return 0, fmt.Errorf("некорректный отрицательный размер файла scp в заголовке %q", line)
		}

		if strings.TrimSpace(parts[2]) == "" {
			return 0, fmt.Errorf("пустое имя файла scp в заголовке %q", line)
		}

		return size, nil
	default:
		return 0, fmt.Errorf("неожиданный префикс scp-заголовка: %q", firstByte)
	}
}

//nolint:mnd
func isValidSCPFileMode(mode string) bool {
	if len(mode) != 4 {
		return false
	}

	for _, r := range mode {
		if r < '0' || r > '7' {
			return false
		}
	}

	return true
}

//nolint:mnd
func readSCPAck(reader *bufio.Reader) error {
	ack, err := reader.ReadByte()
	if err != nil {
		return err
	}

	switch ack {
	case 0:
		return nil
	case 0x01, 0x02:
		message, readErr := reader.ReadString('\n')
		if readErr != nil {
			return readErr
		}

		trimmedMessage := strings.TrimSpace(message)
		if trimmedMessage == "" {
			return errors.New("неизвестная ошибка scp")
		}

		return errors.New(trimmedMessage)
	default:
		return fmt.Errorf("неожиданное значение scp ack: %d", ack)
	}
}

func shellQuote(input string) string {
	if input == "" {
		return "''"
	}

	return "'" + strings.ReplaceAll(input, "'", "'\\''") + "'"
}
