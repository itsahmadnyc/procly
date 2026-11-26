package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// secret returns a secret option without forcing it onto the command line,
// where it shows in ps for as long as the command runs and may stay in the
// shell history:
//
//	--flag value   as before
//	--flag -       the first line of standard input
//	(no flag)      the environment variable env
func secret(a args, flag, env string, stdin io.Reader) (string, error) {
	switch v := a.get(flag); v {
	case "-":
		line, err := bufio.NewReader(stdin).ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			if err != nil && err != io.EOF {
				return "", fmt.Errorf("--%s -: reading standard input: %v", flag, err)
			}
			return "", fmt.Errorf("--%s -: nothing on standard input", flag)
		}
		return line, nil
	case "":
		return strings.TrimSpace(os.Getenv(env)), nil
	default:
		return v, nil
	}
}
