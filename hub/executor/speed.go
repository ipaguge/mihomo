package executor

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/constant"
	"github.com/miekg/dns"
	log "github.com/sirupsen/logrus"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// By default, we average over a one-minute period, which means the average
	// age of the metrics in the period is 30 seconds.
	AVG_METRIC_AGE float64 = 30.0

	// The formula for computing the decay factor from the average age comes
	// from "Production and Operations Analysis" by Steven Nahmias.
	DECAY float64 = 2 / (float64(AVG_METRIC_AGE) + 1)

	// For best results, the moving average should not be initialized to the
	// samples it sees immediately. The book "Production and Operations
	// Analysis" by Steven Nahmias suggests initializing the moving average to
	// the mean of the first 10 samples. Until the VariableEwma has seen this
	// many samples, it is not "ready" to be queried for the value of the
	// moving average. This adds some memory cost.
	WARMUP_SAMPLES uint8 = 10
)

// MovingAverage is the interface that computes a moving average over a time-
// series stream of numbers. The average may be over a window or exponentially
// decaying.
type MovingAverage interface {
	Add(float64)
	Value() float64
	Set(float64)
}

// NewMovingAverage constructs a MovingAverage that computes an average with the
// desired characteristics in the moving window or exponential decay. If no
// age is given, it constructs a default exponentially weighted implementation
// that consumes minimal memory. The age is related to the decay factor alpha
// by the formula given for the DECAY constant. It signifies the average age
// of the samples as time goes to infinity.
func NewMovingAverage(age ...float64) MovingAverage {
	if len(age) == 0 || age[0] == AVG_METRIC_AGE {
		return new(SimpleEWMA)
	}
	return &VariableEWMA{
		decay: 2 / (age[0] + 1),
	}
}

// A SimpleEWMA represents the exponentially weighted moving average of a
// series of numbers. It WILL have different behavior than the VariableEWMA
// for multiple reasons. It has no warm-up period and it uses a constant
// decay.  These properties let it use less memory.  It will also behave
// differently when it's equal to zero, which is assumed to mean
// uninitialized, so if a value is likely to actually become zero over time,
// then any non-zero value will cause a sharp jump instead of a small change.
// However, note that this takes a long time, and the value may just
// decays to a stable value that's close to zero, but which won't be mistaken
// for uninitialized. See http://play.golang.org/p/litxBDr_RC for example.
type SimpleEWMA struct {
	// The current value of the average. After adding with Add(), this is
	// updated to reflect the average of all values seen thus far.
	value float64
}

// Add adds a value to the series and updates the moving average.
func (e *SimpleEWMA) Add(value float64) {
	if e.value == 0 { // this is a proxy for "uninitialized"
		e.value = value
	} else {
		e.value = (value * DECAY) + (e.value * (1 - DECAY))
	}
}

// Value returns the current value of the moving average.
func (e *SimpleEWMA) Value() float64 {
	return e.value
}

// Set sets the EWMA's value.
func (e *SimpleEWMA) Set(value float64) {
	e.value = value
}

// VariableEWMA represents the exponentially weighted moving average of a series of
// numbers. Unlike SimpleEWMA, it supports a custom age, and thus uses more memory.
type VariableEWMA struct {
	// The multiplier factor by which the previous samples decay.
	decay float64
	// The current value of the average.
	value float64
	// The number of samples added to this instance.
	count uint8
}

// Add adds a value to the series and updates the moving average.
func (e *VariableEWMA) Add(value float64) {
	switch {
	case e.count < WARMUP_SAMPLES:
		e.count++
		e.value += value
	case e.count == WARMUP_SAMPLES:
		e.count++
		e.value = e.value / float64(WARMUP_SAMPLES)
		e.value = (value * e.decay) + (e.value * (1 - e.decay))
	default:
		e.value = (value * e.decay) + (e.value * (1 - e.decay))
	}
}

// Value returns the current value of the average, or 0.0 if the series hasn't
// warmed up yet.
func (e *VariableEWMA) Value() float64 {
	if e.count <= WARMUP_SAMPLES {
		return 0.0
	}

	return e.value
}

// Set sets the EWMA's value.
func (e *VariableEWMA) Set(value float64) {
	e.value = value
	if e.count <= WARMUP_SAMPLES {
		e.count = WARMUP_SAMPLES + 1
	}
}

type Queue struct {
	list  *list.List
	mutex sync.Mutex
}

func GetQueue() *Queue {
	return &Queue{
		list: list.New(),
	}
}

func (queue *Queue) Push(data interface{}) {
	if data == nil {
		return
	}
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	queue.list.PushBack(data)
}

func (queue *Queue) Pop() (interface{}, error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	if element := queue.list.Front(); element != nil {
		queue.list.Remove(element)
		return element.Value, nil
	}
	return nil, errors.New("pop failed")
}

func (queue *Queue) Clear() {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	for element := queue.list.Front(); element != nil; {
		elementNext := element.Next()
		queue.list.Remove(element)
		element = elementNext
	}
}

func (queue *Queue) Len() int {
	return queue.list.Len()
}

func (queue *Queue) Show() {
	for item := queue.list.Front(); item != nil; item = item.Next() {
		fmt.Println(item.Value)
	}
}

type SafeArray struct {
	list []*pingStats
	lock sync.Mutex
}

func NewSafeList() *SafeArray {
	return &SafeArray{list: make([]*pingStats, 0)}
}

func (sl *SafeArray) Push(value *pingStats) {
	sl.lock.Lock()
	defer sl.lock.Unlock()
	sl.list = append(sl.list, value)
}

func (sl *SafeArray) PushLimit(value *pingStats) {
	sl.lock.Lock()
	defer sl.lock.Unlock()
	if len(sl.list) >= 30 {
		sl.sliceDownloadWidth()
		v := sl.list[len(sl.list)-1]
		if value.DownloadWidth > v.DownloadWidth {
			v = value
			sl.sliceDownloadWidth()
		}
	} else {
		sl.list = append(sl.list, value)
	}
}

func (sl *SafeArray) Len() int {
	sl.lock.Lock()
	defer sl.lock.Unlock()
	return len(sl.list)
}

func (sl *SafeArray) Iterate(f func(*pingStats)) {
	sl.lock.Lock()
	defer sl.lock.Unlock()
	for _, v := range sl.list {
		f(v)
	}
}

func (sl *SafeArray) sliceDownloadWidth() {
	sort.Slice(sl.list, func(i, j int) bool {
		// 首先按DownloadWidth降序排序
		if sl.list[i].DownloadWidth != sl.list[j].DownloadWidth {
			return sl.list[i].DownloadWidth > sl.list[j].DownloadWidth
		}
		// 如果DownloadWidth相同，则按AveragePing升序排序
		return sl.list[i].HttpPing < sl.list[j].HttpPing
	})
}

func (sl *SafeArray) Ping() {
	sl.lock.Lock()
	defer sl.lock.Unlock()

	newList := make([]*pingStats, 0)
	for _, v := range sl.list {
		downloadWidth, httpPing, _ := downloadHandler(v.Ip)
		if downloadWidth > 0 || httpPing > 0 {
			v.DownloadWidth = downloadWidth
			v.HttpPing = httpPing
			newList = append(newList, v)
		}
	}
	sl.list = newList
	sl.sliceDownloadWidth()
}

func (sl *SafeArray) GetFirst() (value *pingStats) {
	sl.lock.Lock()
	defer sl.lock.Unlock()
	if len(sl.list) == 0 {
		return
	}
	sl.sliceDownloadWidth()
	value = sl.list[0]
	return
}

type CloudflareRes struct {
	Result   CloudflareIPs `json:"result"`
	Success  bool          `json:"success"`
	Errors   []interface{} `json:"errors"`
	Messages []interface{} `json:"messages"`
}

type CloudflareIPs struct {
	Ipv4Cidrs []string `json:"ipv4_cidrs"`
	Ipv6Cidrs []string `json:"ipv6_cidrs"`
	Etag      string   `json:"etag"`
}

type pingStats struct {
	Ip            string
	LossRate      float64
	DownloadWidth float64
	HttpPing      int64
}

func getProxy(ip string) (instance constant.Conn, err error) {
	dataMap := map[string]interface{}{
		"name":     "ss-in",
		"type":     "ss",
		"server":   "127.0.0.1",
		"port":     rawCloudflareDns.INPort,
		"cipher":   "aes-128-gcm",
		"password": "vlmpIPSyHH6f4S8WVPdRIHIlzmB+GIRfoH3aNJ/t9Gg=",
		"udp":      true,
		"smux": map[string]interface{}{
			"enabled":     true,
			"protocol":    "smux",
			"min-streams": 1,
			"max-streams": 20,
		},
	}

	proxy, err := adapter.ParseProxy(dataMap)
	if err != nil {
		return
	}

	instance, err = proxy.DialContext(context.Background(), &constant.Metadata{
		Host:    ip,
		DstIP:   netip.Addr{},
		DstPort: uint16(443),
	})
	return
}

func downloadHandler(ip string) (d float64, httpPing int64, err error) {

	timeout := 800 * time.Millisecond

	instance, err := getProxy(ip)
	if err != nil {
		return
	}

	client := &http.Client{

		Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				return instance, nil
			},
		},
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 10 { // 限制最多重定向 10 次
				return http.ErrUseLastResponse
			}
			req.Header.Del("Referer")
			return nil
		},
	}
	req, err := http.NewRequest("GET", rawCloudflareDns.SpeedUrl, nil)
	if err != nil {
		return
	}
	//req.Host
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_12_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/98.0.4758.80 Safari/537.36")

	pingStart := time.Now()
	response, err := client.Do(req)
	if err != nil {
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return
	}
	pingEnd := time.Now()
	pingDuration := pingEnd.Sub(pingStart)

	start := time.Now()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return
	}
	bytesRead := len(data)
	end := time.Now()

	duration := end.Sub(start)
	seconds := duration.Seconds()
	perSecond := (float64(bytesRead) / 1024) / seconds

	return perSecond, pingDuration.Milliseconds(), nil
}

func pingAll(allIps []string) {

	total := len(allIps)
	var count int64
	queue := GetQueue()
	for _, v := range allIps {
		queue.Push(v)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan bool)
	defer func() {
		done <- true
	}()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("Speed: %v", r)
			}
		}()
		for {
			select {
			case <-done:
				return
			case <-time.After(500 * time.Millisecond):
				log.Debug("Progress: %d/%d okips:%d ", count, total, overallIp.Len())
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Printf("Speed: %v", r)
				}
			}()
			defer func() {
				wg.Done()
			}()

			for {
				time.Sleep(time.Millisecond * 100)
				if ctx.Err() != nil {
					break
				}

				ip, err := queue.Pop()
				if err != nil {
					cancel()
					break
				}
				atomic.AddInt64(&count, 1)
				stats := &pingStats{
					Ip: ip.(string),
				}
				stats.DownloadWidth, stats.HttpPing, err = downloadHandler(ip.(string))
				if err == nil || stats.DownloadWidth > 50 && (stats.HttpPing > 0 && stats.HttpPing < 2000) {
					overallIp.PushLimit(stats)
					if overallIp.Len() >= 30 {
						break
					}
				}
			}

		}()
	}

	wg.Wait()

	return
}

func incrementIP(ip net.IP) []net.IP {
	ips := make([]net.IP, 0)
	rand.Seed(time.Now().UnixNano())
	//for i := 0; i < 2; i++ {
	randomNumber := rand.Intn(240) + 1
	ips = append(ips, incrementIPBy(ip, randomNumber))
	//}
	return ips
}

func incrementIPBy(ip net.IP, count int) net.IP {
	incIP := make(net.IP, len(ip))
	copy(incIP, ip)

	for i := 0; i < count; i++ {
		for j := len(incIP) - 1; j >= 0; j-- {
			incIP[j]++
			if incIP[j] != 0 {
				break
			}
		}
	}
	return incIP
}

func getIps() (allips []string) {
	allips = make([]string, 0)
	resp, err := http.Get(rawCloudflareDns.CloudflareIpsApi)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	var res CloudflareRes
	err = json.Unmarshal(body, &res)
	if err != nil {
		return
	}

	for _, cidr := range res.Result.Ipv4Cidrs {

		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}

		prefixSize, _ := ipNet.Mask.Size()
		numSubnets := 1 << (24 - prefixSize) // 2^(24-prefixSize)

		// Generate the .1 address for each /24 subnet
		for i := 0; i < numSubnets; i++ {
			subnetIP := incrementIPBy(ipNet.IP, i*256) // Move to the next /24 subnet
			firstIPs := incrementIP(subnetIP)          // Get the .1 address (next after network address)
			for _, ip := range firstIPs {
				allips = append(allips, fmt.Sprintf("%s", ip))
			}
		}

	}

	return
}

func handleDNSRequest(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Compress = false

	switch r.Opcode {
	case dns.OpcodeQuery:
		parseQuery(m)
	}

	w.WriteMsg(m)
}

func parseQuery(m *dns.Msg) {
	ipInfo := overallIp.GetFirst()
	for _, q := range m.Question {
		switch q.Qtype {
		case dns.TypeA:
			domainName := q.Name
			if domainName[len(domainName)-1] == '.' {
				domainName = domainName[:len(domainName)-1]
			}
			log.Infof("节点域名:%s 解析", domainName)

			if rawCloudflareDns.Domain != nil {
				_, found := rawCloudflareDns.Domain[domainName]
				if found && ipInfo != nil {
					rr, err := dns.NewRR(fmt.Sprintf("%s A %s", q.Name, ipInfo.Ip))
					if err == nil {
						log.Infof("节点域名:%s CloudflareDns:%s", domainName, ipInfo.Ip)
						m.Answer = append(m.Answer, rr)
						return
					}
				}
			}
			if rawCloudflareDns.DefaultNameserver == nil {
				return
			}

			for _, v := range rawCloudflareDns.DefaultNameserver {
				c := new(dns.Client)
				nm := new(dns.Msg)
				nm.SetQuestion(dns.Fqdn(domainName), dns.TypeA) // TypeA 查询A记录
				nm.RecursionDesired = true

				r, _, err := c.Exchange(nm, v)
				if err != nil {
					log.Errorf("解析节点域名%s失败:%s", domainName, err.Error())
					return
				}
				if len(r.Answer) == 0 {
					log.Errorf("解析节点域名%s失败:no records found", domainName)
					return
				}
				m.Answer = append(m.Answer, r.Answer...)
			}

		}
	}
}

var overallIp = NewSafeList()
var handleChan chan bool
var dnsListens []string
var rawCloudflareDns config.RawCloudflareDns

func stopSpeed() {
	if handleChan != nil {
		handleChan <- true
		handleChan = nil
	}
}

func updateSpeed(h config.RawCloudflareDns) {
	if !h.Enable {
		stopSpeed()
		return
	}
	if handleChan != nil && rawCloudflareDns.Port > 0 && rawCloudflareDns.Port != h.Port {
		rawCloudflareDns = h
		stopSpeed()
		go updateSpeed(h)
		return
	}
	rawCloudflareDns = h
	if rawCloudflareDns.CloudflareIpsApi == "" {
		rawCloudflareDns.CloudflareIpsApi = "https://api.cloudflare.com/client/v4/ips"
	}
	if rawCloudflareDns.DownloadSize == 0 {
		rawCloudflareDns.DownloadSize = 10
	}
	if rawCloudflareDns.SpeedUrl == "" {
		rawCloudflareDns.SpeedUrl = fmt.Sprintf("https://speed.cloudflare.com/__down?bytes=%d", rawCloudflareDns.DownloadSize)
	}

	if handleChan != nil {
		return
	}
	handleChan = make(chan bool)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("Speed: %v", r)
			}
		}()

		defer stopSpeed()

		task := func() {
			overallIp.Ping()
			if overallIp.Len() == 10 {
				allIps := getIps()
				pingAll(allIps)
			}
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Printf("Speed: %v", r)
				}
			}()
			timer := time.NewTimer(time.Second * 60)
			defer timer.Stop()
			time.Sleep(time.Second * 5)
			task()
			if overallIp.Len() == 0 {
				fmt.Printf("该地区无法使用cnd优选加速")
				return
			}
			for {
				<-timer.C
				task()
				if overallIp.Len() == 0 {
					fmt.Printf("该地区无法使用cnd优选加速")
					break
				}
			}
		}()

		server := &dns.Server{Addr: fmt.Sprintf(":%d", rawCloudflareDns.Port), Net: "udp"}
		dns.HandleFunc(".", handleDNSRequest)
		go func() {
			if err := server.ListenAndServe(); err != nil {
				log.Errorf("CloudflareDns:%d 监听失败 %s", rawCloudflareDns.Port, err.Error())
			}
		}()
		log.Infoln("CloudflareDns:%d", rawCloudflareDns.Port)
		<-handleChan
		server.Shutdown()
	}()

	return
}
