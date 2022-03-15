package common

import (
	"strings"

	C "github.com/Dreamacro/clash/constant"
)

type DomainKeyword struct {
	keyword   string
	adapter   string
	ruleExtra *C.RuleExtra
}

func (dk *DomainKeyword) RuleType() C.RuleType {
	return C.DomainKeyword
}

func (dk *DomainKeyword) Match(metadata *C.Metadata) bool {
	if metadata.AddrType != C.AtypDomainName {
		return false
	}
	domain := metadata.Host
	return strings.Contains(domain, dk.keyword)
}

func (dk *DomainKeyword) Adapter() string {
	return dk.adapter
}

func (dk *DomainKeyword) Payload() string {
	return dk.keyword
}

func (dk *DomainKeyword) ShouldResolveIP() bool {
	return false
}

func (dk *DomainKeyword) ShouldFindProcess() bool {
	return false
}

func (dk *DomainKeyword) RuleExtra() *C.RuleExtra {
	return dk.ruleExtra
}

func NewDomainKeyword(keyword string, adapter string, ruleExtra *C.RuleExtra) *DomainKeyword {
	return &DomainKeyword{
		keyword:   strings.ToLower(keyword),
		adapter:   adapter,
		ruleExtra: ruleExtra,
	}
}
