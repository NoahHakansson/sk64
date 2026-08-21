package diff

import (
	"crypto/sha256"
	"fmt"

	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/aymanbagabas/go-udiff"
)

// Render returns a plaintext unified diff or a binary size-and-hash summary.
func Render(oldLabel, newLabel string, before, after []byte, ellipsis string) string {
	if string(before) == string(after) {
		return "(no changes)"
	}
	if k8s.IsBinaryValue(before) || k8s.IsBinaryValue(after) {
		beforeSum := sha256.Sum256(before)
		afterSum := sha256.Sum256(after)
		return fmt.Sprintf("binary value changed\nbefore: %s  sha256 %x%s   after: %s  sha256 %x%s",
			HumanSize(len(before)), beforeSum[:6], ellipsis, HumanSize(len(after)), afterSum[:6], ellipsis)
	}
	result := udiff.Unified(oldLabel, newLabel, string(before), string(after))
	if result == "" {
		return "(no changes)"
	}
	return result
}

// HumanSize formats a byte count using 1024-based binary units.
func HumanSize(size int) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	if size < 1024*1024 {
		return fmt.Sprintf("%.1f KiB", float64(size)/1024)
	}
	return fmt.Sprintf("%.1f MiB", float64(size)/(1024*1024))
}
