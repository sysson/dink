package types

import "time"

const (
	//default http settings
	ReadTimeout       time.Duration = time.Minute
	ReadHeaderTimeout time.Duration = time.Minute
	WriteTimeout      time.Duration = 0
	IdleTimeout       time.Duration = 2 * time.Minute

	APIVersion    string = "1.55"
	MinAPIVersion string = "1.40"
)
