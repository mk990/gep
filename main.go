package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ProxyResult holds the test result for a proxy
type ProxyResult struct {
	Proxy     string
	IsWorking bool
	IP        string
	Error     string
	Duration  time.Duration
}

// ProxyTester handles concurrent proxy testing
type ProxyTester struct {
	timeout     time.Duration
	maxWorkers  int
	testURL     string
}

// NewProxyTester creates a new proxy tester instance
func NewProxyTester(timeout time.Duration, maxWorkers int) *ProxyTester {
	return &ProxyTester{
		timeout:    timeout,
		maxWorkers: maxWorkers,
		testURL:    "http://ifconfig.co/ip",
	}
}

// testProxy tests a single proxy
func (pt *ProxyTester) testProxy(proxyURL string) ProxyResult {
	start := time.Now()
	result := ProxyResult{
		Proxy:     proxyURL,
		IsWorking: false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), pt.timeout)
	defer cancel()

	// Parse proxy URL
	parsedProxy, err := url.Parse(proxyURL)
	if err != nil {
		result.Error = fmt.Sprintf("Invalid proxy URL: %v", err)
		result.Duration = time.Since(start)
		return result
	}

	// Create HTTP client with proxy
	transport := &http.Transport{
		Proxy: http.ProxyURL(parsedProxy),
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   pt.timeout,
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", pt.testURL, nil)
	if err != nil {
		result.Error = fmt.Sprintf("Failed to create request: %v", err)
		result.Duration = time.Since(start)
		return result
	}

	// Set user agent
	req.Header.Set("User-Agent", "ProxyTester/1.0")

	// Make request
	resp, err := client.Do(req)
	if err != nil {
		result.Error = fmt.Sprintf("Request failed: %v", err)
		result.Duration = time.Since(start)
		return result
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		result.Duration = time.Since(start)
		return result
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		result.Error = fmt.Sprintf("Failed to read response: %v", err)
		result.Duration = time.Since(start)
		return result
	}

	// Extract IP from response
	ip := strings.TrimSpace(string(body))
	if net.ParseIP(ip) == nil {
		result.Error = "Invalid IP response"
		result.Duration = time.Since(start)
		return result
	}

	result.IsWorking = true
	result.IP = ip
	result.Duration = time.Since(start)
	return result
}

// TestProxies tests multiple proxies concurrently
func (pt *ProxyTester) TestProxies(proxies []string) []ProxyResult {
	jobs := make(chan string, len(proxies))
	results := make(chan ProxyResult, len(proxies))
	
	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < pt.maxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for proxy := range jobs {
				result := pt.testProxy(proxy)
				results <- result
			}
		}()
	}

	// Send jobs
	for _, proxy := range proxies {
		jobs <- proxy
	}
	close(jobs)

	// Wait for workers to finish
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	var allResults []ProxyResult
	for result := range results {
		allResults = append(allResults, result)
	}

	return allResults
}

// loadProxiesFromReader loads proxies from any reader (file or stdin)
func loadProxiesFromReader(reader io.Reader) ([]string, error) {
	var proxies []string
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			// Add protocol if missing
			if !strings.HasPrefix(line, "http://") && !strings.HasPrefix(line, "https://") && !strings.HasPrefix(line, "socks5://") {
				line = "http://" + line
			}
			proxies = append(proxies, line)
		}
	}

	return proxies, scanner.Err()
}

// loadProxiesFromFile loads proxies from a text file
func loadProxiesFromFile(filename string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return loadProxiesFromReader(file)
}

// isStdinAvailable checks if there's data available on stdin
func isStdinAvailable() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) == 0
}

// printResults prints the test results in a formatted way
func printResults(results []ProxyResult) {
	fmt.Printf("\n%-50s %-10s %-15s %-10s %s\n", "PROXY", "STATUS", "IP", "TIME", "ERROR")
	fmt.Println(strings.Repeat("-", 100))

	workingCount := 0
	for _, result := range results {
		status := "FAILED"
		if result.IsWorking {
			fmt.Println(result.Proxy)
			status = "WORKING"
			workingCount++
		}

		duration := fmt.Sprintf("%.2fs", result.Duration.Seconds())
		
		fmt.Printf("%-50s %-10s %-15s %-10s %s\n", 
			truncateString(result.Proxy, 50), 
			status, 
			result.IP, 
			duration, 
			result.Error)
	}

	fmt.Printf("\nSummary: %d/%d proxies working (%.1f%%)\n", 
		workingCount, len(results), 
		float64(workingCount)/float64(len(results))*100)
}

// truncateString truncates a string to specified length
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func main() {
	// Parse command line flags
	var proxyFile string
	var workers int
	var timeout int
	var showVersion bool

	flag.StringVar(&proxyFile, "l", "", "Load proxies from file")
	flag.StringVar(&proxyFile, "list", "", "Load proxies from file (same as -l)")
	flag.IntVar(&workers, "w", 10, "Number of concurrent workers")
	flag.IntVar(&workers, "workers", 10, "Number of concurrent workers (same as -w)")
	flag.IntVar(&timeout, "t", 15, "Timeout in seconds per proxy")
	flag.IntVar(&timeout, "timeout", 15, "Timeout in seconds per proxy (same as -t)")
	flag.BoolVar(&showVersion, "v", false, "Show version")
	flag.BoolVar(&showVersion, "version", false, "Show version (same as -v)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		fmt.Fprintf(os.Stderr, "  -l, --list FILE     Load proxies from file\n")
		fmt.Fprintf(os.Stderr, "  -w, --workers N     Number of concurrent workers (default: 10)\n")
		fmt.Fprintf(os.Stderr, "  -t, --timeout N     Timeout in seconds per proxy (default: 15)\n")
		fmt.Fprintf(os.Stderr, "  -v, --version       Show version\n")
		fmt.Fprintf(os.Stderr, "  -h, --help          Show this help\n")
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s -l proxies.txt\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  cat proxies.txt | %s\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -l proxies.txt -w 20 -t 10\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nProxy format: ipOrDomain:port (one per line)\n")
	}

	flag.Parse()

	if showVersion {
		fmt.Println("Proxy Tester v1.0")
		return
	}

	fmt.Println("Concurrent Proxy Tester")
	fmt.Println("=======================")

	var proxies []string
	var err error

	// Determine input source
	if proxyFile != "" {
		// Load from file
		fmt.Printf("Loading proxies from file: %s\n", proxyFile)
		proxies, err = loadProxiesFromFile(proxyFile)
		if err != nil {
			fmt.Printf("Error loading proxies from file: %v\n", err)
			os.Exit(1)
		}
	} else if isStdinAvailable() {
		// Load from stdin
		fmt.Println("Reading proxies from stdin...")
		proxies, err = loadProxiesFromReader(os.Stdin)
		if err != nil {
			fmt.Printf("Error reading proxies from stdin: %v\n", err)
			os.Exit(1)
		}
	} else {
		// No input provided
		fmt.Println("No proxy input provided.")
		fmt.Println("\nUsage examples:")
		fmt.Printf("  %s -l proxies.txt\n", os.Args[0])
		fmt.Printf("  cat proxies.txt | %s\n", os.Args[0])
		fmt.Printf("  echo '192.168.1.1:8080' | %s\n", os.Args[0])
		os.Exit(1)
	}

	if len(proxies) == 0 {
		fmt.Println("No proxies to test!")
		os.Exit(1)
	}

	fmt.Printf("Testing %d proxies with %d workers (timeout: %ds)...\n", 
		len(proxies), workers, timeout)

	// Create proxy tester
	tester := NewProxyTester(time.Duration(timeout)*time.Second, workers)

	// Test proxies
	results := tester.TestProxies(proxies)

	// Print results
	printResults(results)

	// Save working proxies to file
	workingProxies := []string{}
	for _, result := range results {
		if result.IsWorking {
			workingProxies = append(workingProxies, result.Proxy)
		}
	}

	if len(workingProxies) > 0 {
		file, err := os.Create("working_proxies.txt")
		if err == nil {
			defer file.Close()
			for _, proxy := range workingProxies {
				file.WriteString(proxy + "\n")
			}
			fmt.Printf("\nWorking proxies saved to: working_proxies.txt\n")
		}
	}
}
