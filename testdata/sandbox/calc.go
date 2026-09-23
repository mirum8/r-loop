package calc

import (
	"strconv"
	"strings"
)

func Add(input string) (int, error) {
	if input == "" {
		return 0, nil
	}
	sum := 0
	for _, part := range strings.Split(input, ",") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, err
		}
		sum += n
	}
	return sum, nil
}
