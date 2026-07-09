package niubiz

import (
	"github.com/qampu/pop/internal/webhook"
)

func init() {
	webhook.Default.Register(Provider, &niubizVerifier{}, &niubizNormalizer{})
}
