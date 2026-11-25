package engine

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ParseCommandFile parses a GitHub Actions command file containing key-value pairs
// in single-line (KEY=VAL) or multiline (KEY<<DELIMITER\n...\nDELIMITER) format.
func ParseCommandFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()

	results := make(map[string]string)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	var currentKey, delimiter string
	var multiLine strings.Builder
	inMultiLine := false

	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimRight(line, "\r")
		if inMultiLine {
			if line == delimiter {
				results[currentKey] = strings.TrimSuffix(multiLine.String(), "\n")
				inMultiLine = false
				multiLine.Reset()
			} else {
				multiLine.WriteString(line + "\n")
			}
			continue
		}

		heredocIdx := strings.Index(line, "<<")
		eqIdx := strings.Index(line, "=")

		if heredocIdx != -1 && (eqIdx == -1 || heredocIdx < eqIdx) {
			currentKey = strings.TrimSpace(line[:heredocIdx])
			if currentKey == "" {
				continue
			}
			delimiter = strings.TrimSpace(line[heredocIdx+2:])
			if delimiter == "" {
				return nil, fmt.Errorf("empty heredoc delimiter for key %q", currentKey)
			}
			inMultiLine = true
			continue
		}

		if eqIdx != -1 {
			k := strings.TrimSpace(line[:eqIdx])
			if k == "" {
				continue
			}
			v := line[eqIdx+1:]
			results[k] = v
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if inMultiLine {
		return nil, fmt.Errorf("unterminated heredoc delimiter %q for key %q", delimiter, currentKey)
	}

	return results, nil
}

// ParseEnvFile parses a file pointed to by $GITHUB_ENV.
func ParseEnvFile(path string) (map[string]string, error) {
	return ParseCommandFile(path)
}

// ParseOutputFile parses a file pointed to by $GITHUB_OUTPUT.
func ParseOutputFile(path string) (map[string]string, error) {
	return ParseCommandFile(path)
}

// ParseEnvAndOutputFile parses a file pointed to by $GITHUB_ENV or $GITHUB_OUTPUT.
func ParseEnvAndOutputFile(path string) (map[string]string, error) {
	return ParseCommandFile(path)
}

// ParsePathFile parses a file pointed to by $GITHUB_PATH where each line is a directory path.
func ParsePathFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var paths []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			paths = append(paths, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return paths, nil
}
