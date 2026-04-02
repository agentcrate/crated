// AgentCrate Playground — WebSocket Chat Client

(() => {
    'use strict';

    const $ = (sel) => document.querySelector(sel);
    const messagesEl = $('#messages');
    const inputForm = $('#inputForm');
    const inputEl = $('#messageInput');
    const sendBtn = $('#sendBtn');
    const statusDot = $('.status-dot');
    const statusText = $('.status-text');
    const statsToggle = $('#statsToggle');
    const statsPanel = $('#statsPanel');

    let ws = null;
    let reconnectDelay = 1000;
    let isStreaming = false;
    let currentAgentMsg = null;

    // ── Stats Tracking ───────────────────────────────────────────────

    const stats = {
        totalTokens: 0,
        promptTokens: 0,
        completionTokens: 0,
        messageCount: 0,
        responseTimes: [],
        tokenHistory: [],
        sessionStart: Date.now(),
        turnStart: 0,
    };

    function updateStatsUI() {
        $('#statTotalTokens').textContent = formatNumber(stats.totalTokens);
        $('#statPromptTokens').textContent = formatNumber(stats.promptTokens);
        $('#statCompletionTokens').textContent = formatNumber(stats.completionTokens);
        $('#statMessages').textContent = stats.messageCount;

        if (stats.responseTimes.length > 0) {
            const avg = stats.responseTimes.reduce((a, b) => a + b, 0) / stats.responseTimes.length;
            $('#statAvgTime').textContent = avg < 1000 ? `${Math.round(avg)}ms` : `${(avg / 1000).toFixed(1)}s`;
        }

        drawTokenChart();
    }

    function formatNumber(n) {
        if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
        if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
        return n.toString();
    }

    // ── Token Chart ──────────────────────────────────────────────────

    function drawTokenChart() {
        const canvas = $('#tokenChart');
        if (!canvas) return;
        const ctx = canvas.getContext('2d');
        const dpr = window.devicePixelRatio || 1;
        const rect = canvas.getBoundingClientRect();

        canvas.width = rect.width * dpr;
        canvas.height = rect.height * dpr;
        ctx.scale(dpr, dpr);

        const w = rect.width;
        const h = rect.height;
        const data = stats.tokenHistory;

        ctx.clearRect(0, 0, w, h);

        if (data.length < 2) {
            ctx.fillStyle = 'hsl(230, 10%, 40%)';
            ctx.font = '11px Inter, system-ui';
            ctx.textAlign = 'center';
            ctx.fillText('Tokens will appear here', w / 2, h / 2 + 4);
            return;
        }

        const max = Math.max(...data, 1);
        const padding = 8;
        const chartW = w - padding * 2;
        const chartH = h - padding * 2;

        // Draw area fill.
        ctx.beginPath();
        ctx.moveTo(padding, padding + chartH);
        data.forEach((v, i) => {
            const x = padding + (i / (data.length - 1)) * chartW;
            const y = padding + chartH - (v / max) * chartH;
            ctx.lineTo(x, y);
        });
        ctx.lineTo(padding + chartW, padding + chartH);
        ctx.closePath();

        const gradient = ctx.createLinearGradient(0, padding, 0, padding + chartH);
        gradient.addColorStop(0, 'hsla(245, 82%, 67%, 0.3)');
        gradient.addColorStop(1, 'hsla(245, 82%, 67%, 0.02)');
        ctx.fillStyle = gradient;
        ctx.fill();

        // Draw line.
        ctx.beginPath();
        data.forEach((v, i) => {
            const x = padding + (i / (data.length - 1)) * chartW;
            const y = padding + chartH - (v / max) * chartH;
            if (i === 0) ctx.moveTo(x, y);
            else ctx.lineTo(x, y);
        });
        ctx.strokeStyle = 'hsl(245, 82%, 67%)';
        ctx.lineWidth = 2;
        ctx.lineJoin = 'round';
        ctx.stroke();

        // Draw dots on recent points.
        const last = data.length - 1;
        const x = padding + (last / (data.length - 1)) * chartW;
        const y = padding + chartH - (data[last] / max) * chartH;
        ctx.beginPath();
        ctx.arc(x, y, 3, 0, Math.PI * 2);
        ctx.fillStyle = 'hsl(245, 82%, 67%)';
        ctx.fill();
    }

    // ── Session Duration Timer ───────────────────────────────────────

    setInterval(() => {
        const elapsed = Math.floor((Date.now() - stats.sessionStart) / 1000);
        const mins = Math.floor(elapsed / 60);
        const secs = elapsed % 60;
        $('#statDuration').textContent = `${mins}:${secs.toString().padStart(2, '0')}`;
    }, 1000);

    // ── Stats Toggle ─────────────────────────────────────────────────

    statsToggle.addEventListener('click', () => {
        statsPanel.classList.toggle('visible');
        statsToggle.classList.toggle('active');
        if (statsPanel.classList.contains('visible')) {
            drawTokenChart();
        }
    });

    // ── WebSocket Connection ─────────────────────────────────────────

    function connect() {
        const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
        ws = new WebSocket(`${proto}//${location.host}/ws`);

        ws.onopen = () => {
            reconnectDelay = 1000;
            setStatus('connected', 'Connected');
            sendBtn.disabled = false;
            inputEl.focus();
        };

        ws.onmessage = (e) => {
            try {
                const event = JSON.parse(e.data);
                handleEvent(event);
            } catch (err) {
                console.error('Failed to parse event:', err);
            }
        };

        ws.onclose = () => {
            setStatus('', 'Disconnected');
            sendBtn.disabled = true;
            setTimeout(() => {
                reconnectDelay = Math.min(reconnectDelay * 2, 10000);
                connect();
            }, reconnectDelay);
        };

        ws.onerror = () => {
            setStatus('error', 'Connection error');
        };
    }

    function setStatus(state, text) {
        statusDot.className = 'status-dot' + (state ? ' ' + state : '');
        statusText.textContent = text;
    }

    // ── Event Handling ───────────────────────────────────────────────

    function handleEvent(event) {
        switch (event.type) {
            case 'text':
                appendAgentText(event.text);
                break;

            case 'tool_call':
                appendToolCall(event.tools);
                break;

            case 'done':
                // Record token usage from the server.
                if (event.totalTokens) {
                    stats.totalTokens += event.totalTokens;
                    stats.promptTokens += event.promptTokens || 0;
                    stats.completionTokens += event.completionTokens || 0;
                    stats.tokenHistory.push(event.totalTokens);
                    // Keep only last 20 data points.
                    if (stats.tokenHistory.length > 20) stats.tokenHistory.shift();
                }
                // Record response time.
                if (stats.turnStart) {
                    stats.responseTimes.push(Date.now() - stats.turnStart);
                    stats.turnStart = 0;
                }
                stats.messageCount++;
                updateStatsUI();
                finishAgentMessage();
                break;

            case 'error':
                appendError(event.text);
                finishAgentMessage();
                break;

            case 'reload':
                appendReloadBanner(event.text);
                break;
        }
    }

    // ── Message Rendering ────────────────────────────────────────────

    function clearWelcome() {
        const welcome = $('.welcome');
        if (welcome) welcome.remove();
    }

    function appendUserMessage(text) {
        clearWelcome();
        const el = createMessage('user', 'You', text);
        messagesEl.appendChild(el);
        scrollToBottom();
    }

    function startAgentMessage() {
        const el = createMessage('agent', 'Agent', '');
        // Add typing indicator.
        const typing = document.createElement('div');
        typing.className = 'typing';
        typing.innerHTML = '<div class="typing-dot"></div><div class="typing-dot"></div><div class="typing-dot"></div>';
        el.querySelector('.message-content').appendChild(typing);
        messagesEl.appendChild(el);
        currentAgentMsg = el;
        scrollToBottom();
    }

    function appendAgentText(text) {
        if (!currentAgentMsg) startAgentMessage();

        const contentEl = currentAgentMsg.querySelector('.message-content');
        const typing = contentEl.querySelector('.typing');
        if (typing) typing.remove();

        const existing = contentEl.getAttribute('data-raw') || '';
        const updated = existing + text;
        contentEl.setAttribute('data-raw', updated);
        contentEl.innerHTML = renderMarkdown(updated);
        scrollToBottom();
    }

    function appendToolCall(tools) {
        if (!currentAgentMsg) startAgentMessage();

        const contentEl = currentAgentMsg.querySelector('.message-content');
        const typing = contentEl.querySelector('.typing');
        if (typing) typing.remove();

        for (const tool of tools) {
            const badge = document.createElement('span');
            badge.className = 'tool-call';
            badge.textContent = tool;
            contentEl.appendChild(badge);
        }
        scrollToBottom();
    }

    function finishAgentMessage() {
        if (currentAgentMsg) {
            const typing = currentAgentMsg.querySelector('.typing');
            if (typing) typing.remove();
        }
        currentAgentMsg = null;
        isStreaming = false;
        sendBtn.disabled = false;
        inputEl.focus();
    }

    function appendError(text) {
        if (!currentAgentMsg) startAgentMessage();
        const contentEl = currentAgentMsg.querySelector('.message-content');
        const typing = contentEl.querySelector('.typing');
        if (typing) typing.remove();
        contentEl.innerHTML = `<span style="color: var(--error)">Error: ${escapeHtml(text)}</span>`;
        scrollToBottom();
    }

    function appendReloadBanner(text) {
        const banner = document.createElement('div');
        banner.className = 'reload-banner';
        banner.textContent = text || 'Agent configuration reloaded';
        messagesEl.appendChild(banner);
        scrollToBottom();
    }

    function createMessage(role, name, text) {
        const el = document.createElement('div');
        el.className = 'message ' + role;

        const avatarText = role === 'user' ? 'U' : '◆';
        el.innerHTML = `
      <div class="message-avatar">${avatarText}</div>
      <div class="message-body">
        <div class="message-role">${escapeHtml(name)}</div>
        <div class="message-content">${text ? renderMarkdown(text) : ''}</div>
      </div>
    `;

        if (text) {
            el.querySelector('.message-content').setAttribute('data-raw', text);
        }
        return el;
    }

    // ── Markdown (basic) ─────────────────────────────────────────────

    function renderMarkdown(text) {
        // Escape HTML first to prevent XSS, then apply markdown formatting.
        const escaped = escapeHtml(text);
        return sanitizeHtml(escaped
            .replace(/```(\w*)\n([\s\S]*?)```/g, '<pre><code>$2</code></pre>')
            .replace(/`([^`]+)`/g, '<code>$1</code>')
            .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
            .replace(/\*(.+?)\*/g, '<em>$1</em>')
            .replace(/\n\n/g, '</p><p>')
            .replace(/\n/g, '<br>')
            .replace(/^/, '<p>')
            .replace(/$/, '</p>')
            .replace(/<p><\/p>/g, ''));
    }

    function sanitizeHtml(html) {
        // Strip script tags and event handlers from rendered HTML.
        return html
            .replace(/<script\b[^<]*(?:(?!<\/script>)<[^<]*)*<\/script>/gi, '')
            .replace(/\bon\w+\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]*)/gi, '');
    }

    function escapeHtml(text) {
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    }

    // ── Input Handling ───────────────────────────────────────────────

    inputForm.addEventListener('submit', (e) => {
        e.preventDefault();
        sendMessage();
    });

    inputEl.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            sendMessage();
        }
    });

    inputEl.addEventListener('input', () => {
        inputEl.style.height = 'auto';
        inputEl.style.height = Math.min(inputEl.scrollHeight, 120) + 'px';
    });

    function sendMessage() {
        const text = inputEl.value.trim();
        if (!text || !ws || ws.readyState !== WebSocket.OPEN || isStreaming) return;

        appendUserMessage(text);
        ws.send(JSON.stringify({ type: 'message', text }));

        isStreaming = true;
        sendBtn.disabled = true;
        inputEl.value = '';
        inputEl.style.height = 'auto';

        // Track response time.
        stats.turnStart = Date.now();

        startAgentMessage();
    }

    function scrollToBottom() {
        requestAnimationFrame(() => {
            messagesEl.scrollTop = messagesEl.scrollHeight;
        });
    }

    // ── Init ─────────────────────────────────────────────────────────

    connect();
})();
