//! A deterministic `ToolInspector` for goose.
//!
//! WHERE THIS GOES
//!   crates/goose/src/permission/toolgate_inspector.rs
//!   registered in the same `ToolInspectionManager` as `PermissionInspector`,
//!   *before* it, so that a command it can decide never reaches the language
//!   model judge.
//!
//! WHY
//!   `permission_judge.rs` classifies unknown tool calls by asking the
//!   configured provider whether the call is read-only, through a synthesised
//!   tool named `platform__tool_by_tool_permission`. That has four costs:
//!
//!     1. a provider round trip on the critical path of every unseen call
//!     2. tokens, billed to the user, to answer a question about their own shell
//!     3. a non-deterministic answer -- the same command can be classified
//!        differently on two consecutive runs
//!     4. the classifier's input is the tool arguments, which the agent composed
//!        after reading files. Text in a repository can therefore influence a
//!        security decision. A false "read-only" verdict means the call runs
//!        with no approval prompt at all.
//!
//!   This inspector answers the common cases in microseconds, from the command
//!   text alone, with no network and no tokens, and returns
//!   `InspectionAction::Allow` only when it can prove the command is read-only.
//!   Everything it cannot decide falls through to the existing judge, so the
//!   change is strictly additive: goose loses no capability, and gains a fast
//!   deterministic path in front of the expensive probabilistic one.
//!
//! FAILURE BEHAVIOUR
//!   `ToolInspectionManager::inspect_tools` logs and continues when an inspector
//!   returns `Err`. This inspector relies on that: if the toolgate process is
//!   missing, crashed, or slow, `inspect` returns `Err` and goose behaves
//!   exactly as it does today. A guard that can take the agent down with it
//!   would be a worse problem than the one it solves.

use anyhow::Result;
use async_trait::async_trait;
use serde::Deserialize;
use std::any::Any;
use std::process::Stdio;
use std::time::Duration;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::process::{Child, ChildStdin, ChildStdout, Command};
use tokio::sync::Mutex;
use tokio::time::timeout;

use crate::config::GooseMode;
use crate::conversation::message::{Message, ToolRequest};
use crate::tool_inspection::{InspectionAction, InspectionResult, ToolInspector};

/// How long the guard gets to answer before goose stops waiting for it.
/// Generous by two orders of magnitude: the analyser's own benchmark is ~13us
/// per command, so anything near this budget means the process is wedged.
const GUARD_TIMEOUT: Duration = Duration::from_millis(250);

/// Tool names whose arguments carry a shell command.
const SHELL_TOOLS: &[&str] = &["developer__shell", "shell", "bash", "run_command"];

#[derive(Debug, Deserialize)]
struct Verdict {
    action: String,
    read_only: bool,
    reason: String,
    findings: Vec<Finding>,
}

#[derive(Debug, Deserialize)]
struct Finding {
    rule_id: String,
    severity: String,
    detail: String,
    #[serde(default)]
    fix: String,
}

/// A long-lived `toolgate serve` child spoken to over MCP stdio.
struct Guard {
    child: Child,
    stdin: ChildStdin,
    stdout: BufReader<ChildStdout>,
    next_id: u64,
}

impl Guard {
    async fn spawn(binary: &str, workspace: &str, policy: &str) -> Result<Self> {
        let mut child = Command::new(binary)
            .args(["serve", "--workspace", workspace, "--policy", policy, "--no-journal"])
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .kill_on_drop(true)
            .spawn()?;

        let stdin = child.stdin.take().expect("stdin piped");
        let stdout = BufReader::new(child.stdout.take().expect("stdout piped"));
        Ok(Self { child, stdin, stdout, next_id: 1 })
    }

    async fn audit(&mut self, command: &str) -> Result<Verdict> {
        let id = self.next_id;
        self.next_id += 1;

        let req = serde_json::json!({
            "jsonrpc": "2.0",
            "id": id,
            "method": "tools/call",
            "params": {
                "_meta": { "protocolVersion": "2026-07-28",
                           "client": { "name": "goose", "version": env!("CARGO_PKG_VERSION") } },
                "name": "audit_command",
                "arguments": { "command": command }
            }
        });

        // One line out, one line back. Dispatch inside toolgate is serial, so
        // responses arrive in request order and no correlation table is needed.
        let mut line = serde_json::to_vec(&req)?;
        line.push(b'\n');
        self.stdin.write_all(&line).await?;
        self.stdin.flush().await?;

        let mut buf = String::new();
        self.stdout.read_line(&mut buf).await?;

        let resp: serde_json::Value = serde_json::from_str(&buf)?;
        let structured = resp
            .pointer("/result/structuredContent")
            .ok_or_else(|| anyhow::anyhow!("toolgate returned no structured verdict"))?;
        Ok(serde_json::from_value(structured.clone())?)
    }
}

pub struct ToolgateInspector {
    guard: Mutex<Option<Guard>>,
    binary: String,
    workspace: String,
    policy: String,
    enabled: bool,
}

impl ToolgateInspector {
    pub fn new(binary: String, workspace: String, policy: String, enabled: bool) -> Self {
        Self { guard: Mutex::new(None), binary, workspace, policy, enabled }
    }

    /// Pull the shell command out of a tool request, if it is one.
    fn command_of(req: &ToolRequest) -> Option<String> {
        let call = req.tool_call.as_ref().ok()?;
        if !SHELL_TOOLS.iter().any(|t| call.name.ends_with(t)) {
            return None;
        }
        call.arguments
            .get("command")
            .and_then(|v| v.as_str())
            .map(str::to_owned)
    }

    fn to_result(req_id: &str, v: &Verdict) -> InspectionResult {
        let action = match v.action.as_str() {
            "deny" => InspectionAction::Deny,
            "require_approval" => {
                let mut why = v.reason.clone();
                if let Some(f) = v.findings.first() {
                    why = format!("{} [{}] {}", f.rule_id, f.severity, f.detail);
                    if !f.fix.is_empty() {
                        why.push_str(&format!(" -- try: {}", f.fix));
                    }
                }
                InspectionAction::RequireApproval(Some(why))
            }
            // Allow is only returned when the analyser proved the command is
            // read-only. An "allow" that merely means "no rule fired" would let
            // a state-changing command through without review, which is a
            // stronger claim than this inspector is entitled to make.
            _ if v.read_only => InspectionAction::Allow,
            _ => InspectionAction::RequireApproval(None),
        };

        InspectionResult {
            tool_request_id: req_id.to_string(),
            action,
            reason: v.reason.clone(),
            // Deterministic: the same input yields the same verdict, always.
            // Compare with the language-model judge, whose confidence is neither
            // reported nor stable.
            confidence: 1.0,
            inspector_name: "toolgate".to_string(),
            finding_id: v.findings.first().map(|f| f.rule_id.clone()),
        }
    }
}

#[async_trait]
impl ToolInspector for ToolgateInspector {
    fn name(&self) -> &'static str {
        "toolgate"
    }

    fn is_enabled(&self) -> bool {
        self.enabled
    }

    async fn inspect(
        &self,
        _session_id: &str,
        tool_requests: &[ToolRequest],
        _messages: &[Message],
        goose_mode: GooseMode,
    ) -> Result<Vec<InspectionResult>> {
        // Chat mode runs no tools, and Auto mode is an explicit decision by the
        // user to skip inspection. Neither is this inspector's business.
        if matches!(goose_mode, GooseMode::Chat | GooseMode::Auto) {
            return Ok(Vec::new());
        }

        let mut guard = self.guard.lock().await;
        if guard.is_none() {
            *guard = Some(Guard::spawn(&self.binary, &self.workspace, &self.policy).await?);
        }
        let g = guard.as_mut().expect("spawned above");

        let mut out = Vec::new();
        for req in tool_requests {
            let Some(command) = Self::command_of(req) else { continue };

            match timeout(GUARD_TIMEOUT, g.audit(&command)).await {
                Ok(Ok(v)) => out.push(Self::to_result(&req.id, &v)),
                Ok(Err(e)) => {
                    // The child is in an unknown state; drop it so the next call
                    // starts a fresh one, and let the manager carry on without us.
                    let _ = g.child.start_kill();
                    *guard = None;
                    return Err(e);
                }
                Err(_) => {
                    let _ = g.child.start_kill();
                    *guard = None;
                    return Err(anyhow::anyhow!("toolgate did not answer within {GUARD_TIMEOUT:?}"));
                }
            }
        }
        Ok(out)
    }

    fn as_any(&self) -> &dyn Any {
        self
    }
}
