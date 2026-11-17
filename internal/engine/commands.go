package engine

import (
	"bufio"
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

	var currentKey, delimiter string
	var multiLine strings.Builder
	inMultiLine := false

	for scanner.Scan() {
		line := scanner.Text()
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

		if idx := strings.Index(line, "<<"); idx != -1 {
			currentKey = strings.TrimSpace(line[:idx])
			delimiter = strings.TrimSpace(line[idx+2:])
			inMultiLine = true
			continue
		}

		if idx := strings.Index(line, "="); idx != -1 {
			k := strings.TrimSpace(line[:idx])
			v := line[idx+1:]
			results[k] = v
		}
	}

	return results, scanner.Err()
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
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			paths = append(paths, line)
		}
	}

	return paths, scanner.Err()
}
