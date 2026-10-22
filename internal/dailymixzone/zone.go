// Package dailymixzone 保存每日任务共同使用的 UTC+8 时区。
package dailymixzone

import "time"

var Zone = time.FixedZone("UTC+8", 8*60*60)
