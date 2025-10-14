package questions

import (
	"errors"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

// Question represents a multiple choice interview question.
type Question struct {
	Topic       string
	Level       string
	Prompt      string
	Options     []string
	Answer      string
	Explanation string
}

// Bank stores the question collection and provides helper methods.
type Bank struct {
	mu        sync.RWMutex
	questions []Question
	rnd       *rand.Rand
}

// DefaultBank returns an in-memory bank pre-populated with curated DevOps questions.
func DefaultBank() *Bank {
	q := make([]Question, len(defaultQuestions))
	copy(q, defaultQuestions)

	return &Bank{
		questions: q,
		rnd:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Topics lists all unique topics contained in the bank, sorted alphabetically.
func (b *Bank) Topics() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	set := make(map[string]struct{})
	for _, q := range b.questions {
		set[q.Topic] = struct{}{}
	}

	topics := make([]string, 0, len(set))
	for topic := range set {
		topics = append(topics, topic)
	}
	sort.Strings(topics)
	return topics
}

// Random returns a random question, optionally filtered by topic.
func (b *Bank) Random(topic string) (Question, error) {
	q, _, _, err := b.RandomWithExclusion(topic, nil)
	return q, err
}

// RandomWithExclusion returns a random question excluding specific indexes. If all
// candidates are excluded, it resets the exclusion set and returns a random question
// while signalling a reset via the third return value.
func (b *Bank) RandomWithExclusion(topic string, exclude map[int]struct{}) (Question, int, bool, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var indices []int
	filter := strings.ToLower(strings.TrimSpace(topic))
	for idx, q := range b.questions {
		if filter == "" || strings.ToLower(q.Topic) == filter {
			indices = append(indices, idx)
		}
	}

	if len(indices) == 0 {
		return Question{}, 0, false, errors.New("no questions available for the requested topic")
	}

	available := make([]int, 0, len(indices))
	if len(exclude) > 0 {
		for _, idx := range indices {
			if _, used := exclude[idx]; !used {
				available = append(available, idx)
			}
		}
	}

	reset := false
	pool := available
	if len(pool) == 0 {
		pool = indices
		reset = len(exclude) > 0
	}

	idx := pool[b.rnd.Intn(len(pool))]
	return b.questions[idx], idx, reset, nil
}

// AddQuestion appends a question to the bank at runtime.
func (b *Bank) AddQuestion(q Question) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.questions = append(b.questions, q)
}

var defaultQuestions = []Question{
	{
		Topic:       "Ansible",
		Level:       "Mid",
		Prompt:      "You need to ensure that a specific handler runs only once after all hosts have completed their tasks. Which Ansible feature do you use?",
		Options:     []string{"Use `serial` to limit execution", "Set `run_once: true` on the handler", "Wrap the handler in a `block`", "Convert the handler into a separate playbook"},
		Answer:      "Set `run_once: true` on the handler",
		Explanation: "Handlers run per host; setting `run_once: true` ensures the handler executes a single time after all hosts finish, which avoids repeated operations like restarting shared services.",
	},
	{
		Topic:       "Ansible",
		Level:       "Senior",
		Prompt:      "What is the recommended way to pass secrets to Ansible without exposing them in plain text?",
		Options:     []string{"Use inventory variables", "Store secrets in group_vars", "Use Ansible Vault", "Store secrets in plaintext and rely on TLS"},
		Answer:      "Use Ansible Vault",
		Explanation: "Ansible Vault encrypts sensitive data such as passwords or keys, allowing secure storage in version control while decrypting at runtime.",
	},
	{
		Topic:       "Docker",
		Level:       "Mid",
		Prompt:      "Which Dockerfile instruction invalidates the build cache for all subsequent instructions when its contents change?",
		Options:     []string{"`RUN`", "`COPY`", "`FROM`", "`ENTRYPOINT`"},
		Answer:      "`COPY`",
		Explanation: "When files copied into the image change, Docker invalidates the cache for that layer and all following layers, triggering rebuilds.",
	},
	{
		Topic:       "Docker",
		Level:       "Senior",
		Prompt:      "How can you reduce the size of a multi-stage Docker build while keeping debugging tools available only during build time?",
		Options:     []string{"Use `ARG` instead of `ENV`", "Add debugging tools in the final stage", "Install tools in an intermediate stage and copy artifacts only", "Enable Docker BuildKit"},
		Answer:      "Install tools in an intermediate stage and copy artifacts only",
		Explanation: "Multi-stage builds allow installing tooling in a build stage and copying only the required artifacts into the final runtime stage, keeping the final image slim.",
	},
	{
		Topic:       "Linux",
		Level:       "Mid",
		Prompt:      "You notice high load average but low CPU usage on a Linux host. Which resource is most likely saturated?",
		Options:     []string{"Disk I/O", "Swap", "GPU", "CPU cache"},
		Answer:      "Disk I/O",
		Explanation: "A high load average with low CPU usage often indicates processes waiting on I/O; disk bottlenecks are a common cause.",
	},
	{
		Topic:       "Linux",
		Level:       "Senior",
		Prompt:      "How do you permanently enable process accounting to capture command history across reboots?",
		Options:     []string{"Enable `acct` via `/etc/rc.local`", "Enable auditd rules for execve", "Configure journald persistent storage", "Add the shell history to syslog"},
		Answer:      "Enable auditd rules for execve",
		Explanation: "auditd with execve rules records command execution system-wide and can be configured to persist across reboots, offering more comprehensive tracking than acct.",
	},
	{
		Topic:       "Kubernetes",
		Level:       "Mid",
		Prompt:      "Which Kubernetes object ensures a specified number of pods run while supporting rolling updates?",
		Options:     []string{"ReplicaSet", "Deployment", "StatefulSet", "DaemonSet"},
		Answer:      "Deployment",
		Explanation: "Deployments manage ReplicaSets and provide rolling update and rollback capabilities, ensuring the desired number of pods are running.",
	},
	{
		Topic:       "Kubernetes",
		Level:       "Senior",
		Prompt:      "What is the recommended way to run periodic jobs that must complete even if the cluster restarts?",
		Options:     []string{"CronJob with `restartPolicy: Never`", "Deployment with a sleep loop", "CronJob with successful job history limit set to 1", "DaemonSet with `ttlSecondsAfterFinished`"},
		Answer:      "CronJob with `restartPolicy: Never`",
		Explanation: "CronJobs create Jobs that survive restarts; setting `restartPolicy: Never` lets Kubernetes restart failed pods until the job succeeds.",
	},
	{
		Topic:       "GitLab CI",
		Level:       "Mid",
		Prompt:      "You want to reuse a job definition across multiple pipelines while customizing variables. What feature should you use?",
		Options:     []string{"YAML anchors", "Child pipelines", "Hidden job templates with `extends`", "Only/except rules"},
		Answer:      "Hidden job templates with `extends`",
		Explanation: "Defining a hidden job (name starts with dot) and using `extends` lets you share job configuration and override specifics like variables or scripts per environment.",
	},
	{
		Topic:       "GitLab CI",
		Level:       "Senior",
		Prompt:      "How can you prevent a GitLab CI pipeline from deploying to production if the infrastructure code merge request lacks approvals?",
		Options:     []string{"Use `when: manual`", "Configure protected environments and approval rules", "Rely on branch naming conventions", "Add pipeline schedules"},
		Answer:      "Configure protected environments and approval rules",
		Explanation: "Protected environments require approved deployments and restrict who can trigger them, ensuring production deploys only happen with the necessary approvals.",
	},
	{
		Topic:       "Bash",
		Level:       "Mid",
		Prompt:      "What is the safest way to iterate over file names containing spaces in Bash?",
		Options:     []string{"Use `for file in $(ls)`", "Use `find` with `-print0` and `while IFS= read -r -d ''`", "Use `xargs` without flags", "Set `IFS=$'\\n'` temporarily"},
		Answer:      "Use `find` with `-print0` and `while IFS= read -r -d ''`",
		Explanation: "Using null-separated lists handles any characters in file names safely, avoiding word splitting issues.",
	},
	{
		Topic:       "Bash",
		Level:       "Senior",
		Prompt:      "How can you make a Bash script fail immediately if any command in a pipeline fails?",
		Options:     []string{"Set `set -e` only", "Use `set -o pipefail`", "Wrap the pipeline in a subshell", "Use `set -u`"},
		Answer:      "Use `set -o pipefail`",
		Explanation: "`set -o pipefail` makes the pipeline return the exit status of the last failed command rather than the last command, ensuring errors surface.",
	},
	{
		Topic:       "Python",
		Level:       "Mid",
		Prompt:      "Which Python library is commonly used to manage infrastructure as code through AWS CloudFormation?",
		Options:     []string{"boto3", "fabric", "paramiko", "salt"},
		Answer:      "boto3",
		Explanation: "boto3 is AWS's official SDK for Python, providing high-level clients and resources to manage CloudFormation stacks programmatically.",
	},
	{
		Topic:       "Python",
		Level:       "Senior",
		Prompt:      "How can you package a Python CLI tool for cross-platform distribution without requiring users to manage dependencies?",
		Options:     []string{"Publish to PyPI", "Bundle with `pip-tools`", "Create a standalone binary with PyInstaller", "Provide a requirements.txt file"},
		Answer:      "Create a standalone binary with PyInstaller",
		Explanation: "PyInstaller bundles Python applications into standalone executables that include an embedded interpreter, simplifying distribution.",
	},
	{
		Topic:       "Nginx",
		Level:       "Mid",
		Prompt:      "Which Nginx directive enables sticky sessions when using `ip_hash`?",
		Options:     []string{"`keepalive`", "`proxy_set_header`", "`ip_hash` handles stickiness by itself", "`sticky`"},
		Answer:      "`ip_hash` handles stickiness by itself",
		Explanation: "`ip_hash` in the upstream block routes requests from the same client IP to the same backend without additional directives.",
	},
	{
		Topic:       "Nginx",
		Level:       "Senior",
		Prompt:      "How do you mitigate slow client attacks against Nginx upstreams?",
		Options:     []string{"Increase `client_max_body_size`", "Configure `proxy_read_timeout` to 0", "Use `limit_conn` and `limit_req` with `client_body_timeout`", "Enable SPDY protocol"},
		Answer:      "Use `limit_conn` and `limit_req` with `client_body_timeout`",
		Explanation: "Combining connection and request limits with tight body timeouts protects upstreams from clients holding connections open with slow uploads.",
	},
	{
		Topic:       "HAProxy",
		Level:       "Mid",
		Prompt:      "Which HAProxy mode should you use to load balance HTTP traffic with header inspection?",
		Options:     []string{"`mode tcp`", "`mode http`", "`mode health`", "`mode tunnel`"},
		Answer:      "`mode http`",
		Explanation: "HTTP mode enables HAProxy to parse and manipulate HTTP headers, which is required for features like header routing.",
	},
	{
		Topic:       "HAProxy",
		Level:       "Senior",
		Prompt:      "How can HAProxy detect and remove unhealthy backends using active checks with minimal false positives?",
		Options:     []string{"Enable `option httpchk` with `inter` and `fall` tuned", "Use passive health checks only", "Set `maxconn` on backends", "Use stick tables"},
		Answer:      "Enable `option httpchk` with `inter` and `fall` tuned",
		Explanation: "`option httpchk` performs active health checks; adjusting intervals and failure thresholds balances responsiveness and false positives.",
	},
	{
		Topic:       "Grafana",
		Level:       "Mid",
		Prompt:      "What is the recommended way to provision Grafana dashboards as code?",
		Options:     []string{"Upload JSON via UI", "Use dashboard provisioning with JSON files", "Embed dashboards in Prometheus rules", "Leverage Terraform only"},
		Answer:      "Use dashboard provisioning with JSON files",
		Explanation: "Grafana dashboard provisioning loads dashboards from JSON files on startup, enabling version control and repeatable environments.",
	},
	{
		Topic:       "Prometheus",
		Level:       "Mid",
		Prompt:      "How do you alert on a high error rate for an HTTP service using Prometheus and Alertmanager?",
		Options:     []string{"Write an Alertmanager silence", "Create a PromQL rule that compares error to total requests", "Use Grafana alerting only", "Enable Pushgateway"},
		Answer:      "Create a PromQL rule that compares error to total requests",
		Explanation: "A PromQL recording or alert rule such as `sum(rate(http_requests_total{code=~\"5..\"}[5m])) / sum(rate(http_requests_total[5m])) > 0.05` triggers an alert for high error ratios.",
	},
	{
		Topic:       "ELK",
		Level:       "Mid",
		Prompt:      "Which Elasticsearch data type is best suited for aggregating over request paths?",
		Options:     []string{"`text`", "`keyword`", "`nested`", "`geo_point`"},
		Answer:      "`keyword`",
		Explanation: "`keyword` fields are not analyzed and support efficient aggregations and exact matching, making them ideal for paths or IDs.",
	},
	{
		Topic:       "ELK",
		Level:       "Senior",
		Prompt:      "How can you reduce the storage footprint of time-series indices in Elasticsearch?",
		Options:     []string{"Disable replicas", "Use ILM with rollover and force merge", "Increase shard count", "Disable refresh intervals"},
		Answer:      "Use ILM with rollover and force merge",
		Explanation: "Index Lifecycle Management with rollover and force merge shrinks indices by removing deleted docs and consolidating segments while managing retention.",
	},
	{
		Topic:       "SQL",
		Level:       "Mid",
		Prompt:      "What SQL construct ensures that a subquery runs once per row from another table?",
		Options:     []string{"JOIN", "CTE", "Scalar subquery", "Window function"},
		Answer:      "Scalar subquery",
		Explanation: "A scalar subquery returns a single value per row when used in a select list, executing per outer row.",
	},
	{
		Topic:       "SQL",
		Level:       "Senior",
		Prompt:      "How do you detect write skew in a high-concurrency PostgreSQL system?",
		Options:     []string{"Enable query logging", "Use SERIALIZABLE isolation level and monitor serialization failures", "Add more indexes", "Switch to READ UNCOMMITTED"},
		Answer:      "Use SERIALIZABLE isolation level and monitor serialization failures",
		Explanation: "SERIALIZABLE isolation detects write skew and raises serialization errors, which you can monitor to identify contention patterns.",
	},
	{
		Topic:       "ClickHouse",
		Level:       "Mid",
		Prompt:      "Which ClickHouse engine should you use for high-ingest, append-only time-series data?",
		Options:     []string{"MergeTree", "ReplacingMergeTree", "AggregatingMergeTree", "Memory"},
		Answer:      "MergeTree",
		Explanation: "MergeTree is the general-purpose engine optimized for high ingest and supports partitioning and sorting for time-series workloads.",
	},
	{
		Topic:       "ClickHouse",
		Level:       "Senior",
		Prompt:      "How can you ensure deduplicated results when ingesting data with late-arriving updates in ClickHouse?",
		Options:     []string{"Use SummingMergeTree", "Use ReplacingMergeTree with a version column", "Disable merges", "Enable TTL moves"},
		Answer:      "Use ReplacingMergeTree with a version column",
		Explanation: "ReplacingMergeTree allows deduplication based on a primary key, and providing a version column ensures the latest record wins during merges.",
	},
	{
		Topic:       "DevOps",
		Level:       "Senior",
		Prompt:      "What practice aligns development and operations goals by measuring deployment frequency, lead time, mean time to recovery, and change failure rate?",
		Options:     []string{"ITIL", "DORA metrics", "ISO 27001", "Six Sigma"},
		Answer:      "DORA metrics",
		Explanation: "DORA metrics capture the key performance indicators for high-performing DevOps teams, balancing velocity and stability.",
	},
}
