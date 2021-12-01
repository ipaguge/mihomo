package rules

import (
	"fmt"
	"github.com/Dreamacro/clash/adapter/provider"
	C "github.com/Dreamacro/clash/constant"
	types "github.com/Dreamacro/clash/constant/provider"
)

type RuleSet struct {
	ruleProviderName string
	adapter          string
	ruleProvider     *types.RuleProvider
	ruleExtra        *C.RuleExtra
}

func (rs *RuleSet) RuleType() C.RuleType {
	return C.RuleSet
}

func (rs *RuleSet) Match(metadata *C.Metadata) bool {
	return rs.getProviders().Match(metadata)
}

func (rs *RuleSet) Adapter() string {
	return rs.adapter
}

func (rs *RuleSet) Payload() string {
	return rs.getProviders().Name()
}

func (rs *RuleSet) ShouldResolveIP() bool {
	return rs.getProviders().Behavior() != provider.Domain
}
func (rs *RuleSet) getProviders() types.RuleProvider {
	if rs.ruleProvider == nil {
		rp := provider.RuleProviders()[rs.ruleProviderName]
		rs.ruleProvider = &rp
	}

	return *rs.ruleProvider
}

func (rs *RuleSet) RuleExtra() *C.RuleExtra {
	return rs.ruleExtra
}

func NewRuleSet(ruleProviderName string, adapter string, ruleExtra *C.RuleExtra) (*RuleSet, error) {
	rp, ok := provider.RuleProviders()[ruleProviderName]
	if !ok {
		return nil, fmt.Errorf("rule set %s not found", ruleProviderName)
	}
	return &RuleSet{
		ruleProviderName: ruleProviderName,
		adapter:          adapter,
		ruleProvider:     &rp,
		ruleExtra:        ruleExtra,
	}, nil
}
