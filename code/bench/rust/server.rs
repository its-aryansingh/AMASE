//! Identical workload to bench/hookbench.py and bench/go/server.go.
//! Deliberately std-only: no tokio, no hyper, no serde. A hand-rolled
//! blocking HTTP/1.1 loop is the fairest floor for this workload, which is
//! one small POST at a time from a single local client.

use std::collections::HashMap;
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};

struct Features {
    seen: HashMap<u64, u32>,
    edits: HashMap<String, u32>,
    reads: HashMap<String, u32>,
    error_run: u32,
}

impl Features {
    fn new() -> Self {
        Features {
            seen: HashMap::new(),
            edits: HashMap::new(),
            reads: HashMap::new(),
            error_run: 0,
        }
    }

    fn update(&mut self, tool: &str, target: &str, raw: &[u8]) -> f64 {
        // FNV-1a over (tool, args) — the signature role hashing plays elsewhere
        let mut h: u64 = 0xcbf29ce484222325;
        for b in tool.as_bytes().iter().chain(raw.iter()) {
            h ^= *b as u64;
            h = h.wrapping_mul(0x100000001b3);
        }
        let c = self.seen.entry(h).or_insert(0);
        *c += 1;
        let repeat = *c as f64 / 3.0;

        let thrash = if tool == "Edit" || tool == "Write" {
            let e = self.edits.entry(target.to_string()).or_insert(0);
            *e += 1;
            *e as f64 / 5.0
        } else {
            self.edits.get(target).copied().unwrap_or(0) as f64 / 5.0
        };

        let churn = if tool == "Read" {
            let r = self.reads.entry(target.to_string()).or_insert(0);
            *r += 1;
            *r as f64 / 4.0
        } else {
            self.reads.get(target).copied().unwrap_or(0) as f64 / 4.0
        };

        let errs = self.error_run as f64 / 2.0;
        repeat.max(thrash).max(churn).max(errs)
    }
}

/// Minimal field extraction. Full JSON parsing is not what this benchmark is
/// measuring, and pulling in serde would make the three implementations differ
/// in dependency weight rather than in language.
fn field<'a>(body: &'a [u8], key: &str) -> &'a str {
    let pat = format!("\"{}\":\"", key);
    let hay = match std::str::from_utf8(body) {
        Ok(s) => s,
        Err(_) => return "",
    };
    match hay.find(&pat) {
        Some(i) => {
            let rest = &hay[i + pat.len()..];
            match rest.find('"') {
                Some(j) => &rest[..j],
                None => "",
            }
        }
        None => "",
    }
}

const ALLOW: &[u8] =
    b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}";

fn handle(mut stream: TcpStream, feats: &mut Features) {
    let _ = stream.set_nodelay(true);
    let mut buf = vec![0u8; 16384];
    let mut filled = 0usize;

    loop {
        let n = match stream.read(&mut buf[filled..]) {
            Ok(0) => return,
            Ok(n) => n,
            Err(_) => return,
        };
        filled += n;

        loop {
            let head_end = match find(&buf[..filled], b"\r\n\r\n") {
                Some(i) => i,
                None => break,
            };
            let head = &buf[..head_end];
            let clen = content_length(head);
            let total = head_end + 4 + clen;
            if filled < total {
                break;
            }
            let body = &buf[head_end + 4..total];

            let tool = field(body, "tool_name").to_string();
            let target = {
                let c = field(body, "command");
                if !c.is_empty() { c.to_string() } else { field(body, "file_path").to_string() }
            };
            let score = feats.update(&tool, &target, body);

            let resp: Vec<u8> = if score >= 1.0 {
                let b = br#"{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"amase: run predicted to fail"}}"#;
                let mut v = format!(
                    "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n",
                    b.len()
                )
                .into_bytes();
                v.extend_from_slice(b);
                v
            } else {
                ALLOW.to_vec()
            };
            if stream.write_all(&resp).is_err() {
                return;
            }

            buf.copy_within(total..filled, 0);
            filled -= total;
        }
    }
}

fn find(hay: &[u8], needle: &[u8]) -> Option<usize> {
    hay.windows(needle.len()).position(|w| w == needle)
}

fn content_length(head: &[u8]) -> usize {
    let s = match std::str::from_utf8(head) {
        Ok(s) => s,
        Err(_) => return 0,
    };
    for line in s.split("\r\n") {
        if line.len() > 15 && line[..15].eq_ignore_ascii_case("content-length:") {
            return line[15..].trim().parse().unwrap_or(0);
        }
    }
    0
}

fn main() {
    let port: u16 = std::env::args().nth(1).unwrap().parse().unwrap();
    let listener = TcpListener::bind(("127.0.0.1", port)).unwrap();
    let mut feats = Features::new();
    for stream in listener.incoming() {
        match stream {
            Ok(s) => handle(s, &mut feats),
            Err(_) => continue,
        }
    }
}
