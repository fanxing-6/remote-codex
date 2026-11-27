package admin

import "time"

func timeNowPlusDay() time.Time { return time.Now().Add(24 * time.Hour) }
