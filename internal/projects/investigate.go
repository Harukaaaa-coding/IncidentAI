package projects

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (m *Manager) Investigate(ctx context.Context, id, question string) (Investigation, error) {
	p, err := m.Get(id)
	if err != nil {
		return Investigation{}, err
	}
	question = strings.TrimSpace(question)
	if len([]rune(question)) > 2000 {
		return Investigation{}, fmt.Errorf("调查问题不能超过 2000 字")
	}
	if question == "" {
		question = "检查项目当前可见的运行时异常，并给出有证据的只读建议。"
	}
	run := Investigation{ID: newID("run"), ProjectID: id, Question: Redact(question), Status: "completed", StartedAt: time.Now().UTC(), Checks: []Check{}, Evidence: []Evidence{}, Findings: []Finding{}, Hypotheses: []Hypothesis{}, Limitations: []string{
		"本次为本地确定性只读检查，不是 LLM 根因推理；未运行编译/测试，也未验证真实模型服务。未执行目标项目的脚本或代码，也未访问外部模型。",
		"仅保存本轮实际采集的证据片段；后续如人工批准模型分析，模型也只会看到这部分选定证据，而不是整个项目。",
		"只读取受限源码、选定日志尾部和可选本机 HTTP 端点。未接入错误率指标、发布流水线或分布式链路，不能据此确认线上根因或建议自动回滚。",
		"脱敏为尽力而为的模式匹配，并非完整 DLP；源码与日志可能仍包含业务敏感信息。证据只保存在 IncidentAI 本地。",
	}}
	add := func(e Evidence) string {
		e.ID = evidenceID(len(run.Evidence) + 1)
		e.Content = Redact(e.Content)
		e.Summary = Redact(e.Summary)
		run.Evidence = append(run.Evidence, e)
		return e.ID
	}
	addFinding := func(severity, title, detail, recommendation string, ids ...string) {
		run.Findings = append(run.Findings, Finding{ID: fmt.Sprintf("F%d", len(run.Findings)+1), Severity: severity, Title: title, Detail: detail, Recommendation: recommendation, EvidenceIDs: ids})
		run.Status = "needs_attention"
	}
	repo, err := safeRepository(p.RepositoryPath)
	if err != nil {
		return Investigation{}, err
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		return Investigation{}, err
	}
	defer root.Close()
	before, err := scanSources(ctx, root)
	if err != nil {
		return Investigation{}, err
	}
	names := []string{}
	for _, file := range before.files {
		names = append(names, filepath.ToSlash(file.path))
	}
	inventoryID := add(Evidence{Kind: "source_inventory", Source: "bounded local filesystem inspection", Summary: fmt.Sprintf("枚举 %d 个源码文件，跳过 %d 个符号链接、超大或不可读文件", len(before.files), before.skipped), Content: strings.Join(names, "\n")})
	run.Checks = append(run.Checks, Check{"repository", "passed", fmt.Sprintf("读取 %d 个允许类型源码文件；不执行项目代码", len(before.files))})
	if before.limited {
		run.Limitations = append(run.Limitations, "源码扫描达到 400 文件、5000 目录项或 12 层深度上限；未扫描的文件不在结论范围内。")
	}
	if len(before.files) == 0 {
		run.Status = "inconclusive"
		run.Limitations = append(run.Limitations, "没有找到允许读取的源码文件；请检查项目路径与文件类型。")
	}
	logIDs := []string{}
	tokens := []string{}
	tokenSet := map[string]bool{}
	errorCount := 0
	readLogs := 0
	if p.LogPath == "" {
		run.Checks = append(run.Checks, Check{"logs", "skipped", "未配置日志路径"})
		run.Limitations = append(run.Limitations, "没有运行日志，无法确认是否发生了运行时错误。")
	} else if _, err = safeLogPath(repo, p.LogPath); err != nil {
		run.Checks = append(run.Checks, Check{"logs", "failed", err.Error()})
		run.Limitations = append(run.Limitations, "选定日志已不可安全访问；没有将日志缺失解释为系统健康。")
	} else {
		files, err := logFiles(root, p)
		if err != nil {
			run.Checks = append(run.Checks, Check{"logs", "failed", "日志列表不可读"})
		} else {
			for _, path := range files {
				if ctx.Err() != nil {
					return Investigation{}, ctx.Err()
				}
				tail, partial, err := tailLog(root, path)
				if err != nil {
					run.Checks = append(run.Checks, Check{"log:" + filepath.ToSlash(path), "failed", err.Error()})
					continue
				}
				readLogs++
				matches := []string{}
				firstLine := 0
				localCount := 0
				for lineIndex, line := range strings.Split(tail, "\n") {
					if errorLine.MatchString(line) {
						errorCount++
						localCount++
						if firstLine == 0 {
							firstLine = lineIndex + 1
						}
						if len(matches) < 12 {
							if len(line) > 1000 {
								line = line[:1000]
							}
							label := fmt.Sprintf("line %d", lineIndex+1)
							if partial {
								label = fmt.Sprintf("tail-relative line %d", lineIndex+1)
							}
							matches = append(matches, label+": "+line)
						}
						for _, token := range errorToken.FindAllString(line, -1) {
							if !tokenSet[token] && len(tokens) < 3 {
								tokenSet[token] = true
								tokens = append(tokens, token)
							}
						}
					}
				}
				content := strings.Join(matches, "\n")
				summary := fmt.Sprintf("读取尾部最多 %d KiB，发现 %d 行错误/失败/超时关键词", maxLogBytes/1024, localCount)
				if content == "" {
					content = "在选定日志尾部未发现错误关键词；这不证明整个服务无异常。"
				}
				if partial {
					firstLine = 0
					summary += "；文件较大，展示行号相对尾部片段而非完整文件"
					run.Limitations = append(run.Limitations, "日志 "+filepath.ToSlash(path)+" 仅读取最后 256 KiB，早期错误可能未包含。")
				}
				eid := add(Evidence{Kind: "log_tail", Source: "bounded redacted local log tail", Summary: summary, Content: content, Path: filepath.ToSlash(path), Line: firstLine})
				logIDs = append(logIDs, eid)
			}
			if readLogs > 0 {
				run.Checks = append(run.Checks, Check{"logs", "passed", fmt.Sprintf("读取 %d 个日志文件；错误关键词行 %d（并非请求错误率）", readLogs, errorCount)})
			} else {
				run.Checks = append(run.Checks, Check{"logs", "failed", "未取得可读日志内容"})
				run.Limitations = append(run.Limitations, "未取得可读日志，不能验证运行时故障。")
			}
		}
	}
	if errorCount > 0 {
		addFinding("warning", "日志中发现错误信号", fmt.Sprintf("选定日志尾部共 %d 行匹配错误、失败或超时关键词。它们可能来自历史记录、重试或已恢复任务，不能直接视为正在发生的事故。", errorCount), "核对每条记录的时间、run_id 和请求状态；补充事故时间范围内的指标及完整堆栈，再判断是否仍在影响用户。", logIDs...)
	}
	health, body := healthCheck(ctx, p.HealthURL)
	run.Checks = append(run.Checks, health)
	healthID := ""
	if health.Status != "skipped" {
		healthID = add(Evidence{Kind: "health", Source: "loopback-only HTTP GET", Summary: health.Summary, Content: body})
		if health.Status == "failed" {
			addFinding("warning", "配置的健康端点未通过检查", health.Summary, "确认本机服务是否启动、端口与路径是否正确；端点检查失败本身不证明业务根因。", healthID)
		}
	} else {
		run.Limitations = append(run.Limitations, "未配置健康端点；本次未确认项目是否正在运行。")
	}
	status, diff, gitErr := gitEvidence(ctx, p, before)
	gitID := ""
	if gitErr != nil {
		run.Checks = append(run.Checks, Check{"git", "skipped", "Git 基线不可用或受限，未比较版本变更"})
		run.Limitations = append(run.Limitations, "没有完整 Git 变更证据；目录不必是 Git 仓库，不能将缺失差异解释为没有回归。")
	} else {
		summary := "允许读取的源码与 Git HEAD 比较未见未提交差异"
		if strings.TrimSpace(status) != "" || strings.TrimSpace(diff) != "" {
			summary = "允许读取的源码存在相对 Git HEAD 的未提交变更（不是部署记录）"
		}
		gitID = add(Evidence{Kind: "git_diff", Source: "bounded read-only git comparison", Summary: summary, Content: status + "\n" + diff})
		run.Checks = append(run.Checks, Check{"git", "passed", summary})
		if strings.TrimSpace(status) != "" || strings.TrimSpace(diff) != "" {
			addFinding("info", "调查对象存在未提交源码变更", summary, "记录测试对象的版本或快照；不要把本地改动等同于线上已发布版本。", gitID)
		}
	}
	matches, limitations := sourceMatches(ctx, p, before, tokens)
	run.Limitations = append(run.Limitations, limitations...)
	sourceIDs := []string{}
	for _, match := range matches {
		sourceIDs = append(sourceIDs, add(match))
	}
	if len(tokens) > 0 {
		status := "passed"
		if len(limitations) > 0 {
			status = "partial"
		}
		run.Checks = append(run.Checks, Check{"source_search", status, fmt.Sprintf("尝试对 %d 个日志错误标识执行受限 ripgrep 搜索，获得 %d 处源码关联；具体执行限制见下方说明；不据此推断因果", len(tokens), len(matches))})
	}
	refs1 := append([]string{}, logIDs...)
	refs1 = append(refs1, sourceIDs...)
	if len(refs1) == 0 {
		refs1 = []string{inventoryID}
	}
	h1status := "unverified"
	h1assessment := "源码缺陷或配置变化可能导致业务失败；当前没有错误日志、发布对比和指标的完整因果链。"
	if errorCount > 0 {
		h1status = "needs_verification"
		h1assessment = "日志提供了故障信号；搜索到的同名标识最多说明相关实现位置，尚未证明这些代码导致当前用户影响。"
	}
	run.Hypotheses = append(run.Hypotheses, Hypothesis{ID: "H1", Title: "应用逻辑或配置问题", Assessment: h1assessment, Status: h1status, EvidenceIDs: refs1})
	refs2 := append([]string{}, logIDs...)
	if healthID != "" {
		refs2 = append(refs2, healthID)
	}
	if len(refs2) == 0 {
		refs2 = []string{inventoryID}
	}
	run.Hypotheses = append(run.Hypotheses, Hypothesis{ID: "H2", Title: "依赖服务、运行环境或瞬时失败", Assessment: "相同错误也可能由依赖超时、连接失败或运行环境造成。单个本机 HTTP 响应不能排除外部依赖故障；需要事故期间的依赖延迟与错误记录验证。", Status: "unverified", EvidenceIDs: refs2})
	after, err := scanSources(ctx, root)
	if err != nil {
		return Investigation{}, err
	}
	run.SourceChanged = !sameInventory(before, after)
	if run.SourceChanged {
		run.Status = "inconclusive"
		run.Checks = append(run.Checks, Check{"source_stability", "failed", "调查期间可读源码发生变化；证据不是同一稳定快照"})
		run.Limitations = append(run.Limitations, "检测到调查期间源码变化。请待修改完成后重新调查，不应将本轮证据视为一个稳定版本。")
	} else {
		run.Checks = append(run.Checks, Check{"source_stability", "passed", "调查前后允许读取的源码内容指纹一致；这不代表未扫描文件或线上版本相同"})
	}
	if len(run.Findings) == 0 {
		run.Status = "inconclusive"
		run.Limitations = append(run.Limitations, "本轮没有发现足以形成问题结论的证据；端点成功或日志中未见错误，不代表项目没有缺陷。")
	}
	if ctx.Err() != nil {
		return Investigation{}, ctx.Err()
	}
	sort.Strings(tokens)
	run.CompletedAt = time.Now().UTC()
	if err = m.saveRun(run); err != nil {
		return Investigation{}, err
	}
	return run, nil
}
