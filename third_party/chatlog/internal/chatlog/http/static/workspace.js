(() => {
    'use strict';

    const byID = (id) => document.getElementById(id);
    const value = (id) => String(byID(id)?.value || '').trim();
    const numberValue = (id, fallback, minimum = 0, maximum = null) => {
        const raw = value(id);
        let parsed = /^-?\d+$/.test(raw) ? Number(raw) : fallback;
        if (!Number.isSafeInteger(parsed)) parsed = fallback;
        parsed = Math.max(minimum, parsed);
        if (maximum !== null) parsed = Math.min(maximum, parsed);
        const input = byID(id);
        if (input) input.value = String(parsed);
        return parsed;
    };
    let newMessageState = {};
    let newMessageRequest = null;
    let catalogData = null;
    let openAPIData = null;
    let messageSearchNextCursor = '';
    let ocrContacts = [];
    let ocrChatRooms = [];
    let ocrSelectedContacts = new Set();
    let ocrSelectedChatRooms = new Set();
    let ocrConfigLoaded = false;
    let ocrBackfillRequestActive = false;
    let ocrLastMode = 'local';
    const ocrEndpointByMode = {};
    let ocrRetrySyncFeedback = null;

    function setBusy(containerID, text = '正在读取…') {
        const container = byID(containerID);
        if (container) container.innerHTML = `<div class="loading">${text}</div>`;
    }

    function showError(containerID, error) {
        const container = byID(containerID);
        if (!container) return;
        container.replaceChildren();
        const box = document.createElement('div');
        box.className = 'structured-error';
        box.textContent = String(error?.message || error || '请求失败');
        container.appendChild(box);
    }

    async function requestJSON(url, options = {}) {
        const response = await fetch(url, { cache: 'no-store', ...options });
        const text = await response.text();
        let data = {};
        if (text) {
            try {
                data = JSON.parse(text);
            } catch (_) {
                data = { error: text };
            }
        }
        if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
        return data;
    }

    function appendParam(params, name, raw) {
        const normalized = String(raw ?? '').trim();
        if (normalized !== '') params.set(name, normalized);
    }

    function optionalInteger(id, minimum = 0, maximum = null) {
        const raw = value(id);
        if (raw === '') return { valid: true, value: '' };
        if (!/^-?\d+$/.test(raw)) return { valid: false, value: '' };
        const parsed = Number(raw);
        if (!Number.isSafeInteger(parsed) || parsed < minimum || (maximum !== null && parsed > maximum)) {
            return { valid: false, value: '' };
        }
        return { valid: true, value: String(parsed) };
    }

    function absoluteHTTPURL(raw) {
        try {
            const parsed = new URL(String(raw || '').trim());
            return (parsed.protocol === 'http:' || parsed.protocol === 'https:')
                && !!parsed.hostname
                && !parsed.username
                && !parsed.password;
        } catch (_) {
            return false;
        }
    }

    function validRelativeDataPath(raw) {
        const normalized = String(raw || '').trim().replaceAll('\\', '/');
        if (!normalized || normalized.startsWith('/')) return false;
        return !normalized.split('/').some((part) => part === '..');
    }

    function validUint64(raw) {
        const normalized = String(raw || '').trim();
        if (!normalized) return true;
        if (!/^\d+$/.test(normalized)) return false;
        try {
            return BigInt(normalized) <= 18446744073709551615n;
        } catch (_) {
            return false;
        }
    }

    function extractCollection(data) {
        if (Array.isArray(data)) return data;
        if (!data || typeof data !== 'object') return [];
        for (const key of ['messages', 'items', 'sessions', 'members', 'contacts', 'chatrooms', 'notifications', 'events', 'commands']) {
            if (Array.isArray(data[key])) return data[key];
        }
        return [];
    }

    function sameOriginMediaURL(raw) {
        const candidate = String(raw || '').trim();
        if (!candidate) return '';
        try {
            const parsed = new URL(candidate, location.origin);
            if (parsed.origin !== location.origin) return '';
            const allowed = ['/image/', '/video/', '/voice/', '/file/', '/data/', '/api/v1/sns/media/proxy'];
            return allowed.some((prefix) => parsed.pathname.startsWith(prefix)) ? parsed.href : '';
        } catch (_) {
            return '';
        }
    }

    function mediaReferences(item) {
        const references = [];
        const push = (type, rawURL, label) => {
            const url = sameOriginMediaURL(rawURL);
            if (!url || references.some((entry) => entry.url === url)) return;
            references.push({ type: String(type || '').toLowerCase(), url, label: String(label || '媒体') });
        };
        const contents = item.contents && typeof item.contents === 'object' ? item.contents : {};
        // The resolved database-relative proxy points at the concrete WeChat
        // cache entry. Keep it ahead of MD5 routes, which are fallback aliases
        // and must not be rendered as additional media.
        push(contents.proxyType, contents.proxyUrl, contents.proxyType);
        push(item.media_type, item.media_path, item.media_type);
        push(item.media_type, item.media_url, item.media_type);
        push('image', item.image_path, '图片');
        push('image', item.image_url, '图片');
        if (!references.length && Array.isArray(item.media_keys)) {
            item.media_keys.some((key, index) => {
                const type = String(item.media_type || '').trim().toLowerCase();
                const value = String(key || '').trim().replaceAll('\\', '/');
                if (!type || !value) return false;
                push(type, `/${type}/${value.split('/').map(encodeURIComponent).join('/')}`, `${type} 候选 ${index + 1}`);
                return references.length > 0;
            });
        }
        if (!references.length && Array.isArray(item.image_keys)) {
            item.image_keys.some((key, index) => {
                const value = String(key || '').trim().replaceAll('\\', '/');
                if (!value) return false;
                push('image', `/image/${value.split('/').map(encodeURIComponent).join('/')}`, `图片候选 ${index + 1}`);
                return references.length > 0;
            });
        }
        if (Array.isArray(item.media_list)) {
            item.media_list.forEach((media, index) => {
                if (!media || typeof media !== 'object') return;
                push(media.type, media.proxy_url || media.resolved_url || media.url, `朋友圈媒体 ${index + 1}`);
            });
        }
        return references;
    }

    function mediaElement(reference, fallbackReferences = []) {
        const wrapper = document.createElement('div');
        wrapper.className = 'inline-media';
        let element;
        if (reference.type === 'image' || reference.url.includes('/image/')) {
            element = document.createElement('img');
            element.loading = 'lazy';
            element.alt = reference.label;
        } else if (reference.type === 'video' || reference.url.includes('/video/')) {
            element = document.createElement('video');
            element.controls = true;
            element.preload = 'metadata';
        } else if (reference.type === 'voice' || reference.type === 'audio' || reference.url.includes('/voice/')) {
            element = document.createElement('audio');
            element.controls = true;
            element.preload = 'metadata';
        } else {
            element = document.createElement('a');
            element.textContent = `下载${reference.label}`;
            element.className = 'btn btn-secondary btn-sm';
            element.target = '_blank';
            element.rel = 'noopener';
        }
        const candidates = [reference, ...fallbackReferences].filter((candidate, index, values) =>
            candidate?.url && values.findIndex((entry) => entry?.url === candidate.url) === index
        );
        let candidateIndex = 0;
        const applyCandidate = () => {
            const candidate = candidates[candidateIndex] || reference;
            element.src = candidate.url;
            if (element.tagName === 'A') element.href = candidate.url;
        };
        if (element.tagName !== 'A' && candidates.length > 1) {
            element.addEventListener('error', () => {
                if (candidateIndex + 1 >= candidates.length) return;
                candidateIndex += 1;
                applyCandidate();
            });
        }
        applyCandidate();
        wrapper.appendChild(element);
        return wrapper;
    }

    const messageTypeLabels = {
        1: ['文本', '✎'],
        3: ['图片', '▧'],
        34: ['语音', '◖'],
        42: ['名片', '◎'],
        43: ['视频', '▶'],
        47: ['动画表情', '☺'],
        48: ['位置', '⌖'],
        49: ['分享', '↗'],
        50: ['语音通话', '☎'],
        10000: ['系统消息', '◉'],
        11000: ['系统通知', '◉'],
    };

    const shareTypeLabels = {
        1: '分享文本', 4: '链接', 5: '链接', 6: '文件', 8: 'GIF 表情',
        17: '实时位置', 19: '合并转发', 24: '笔记', 33: '小程序',
        36: '小程序', 51: '视频号', 57: '引用回复', 62: '拍一拍',
        63: '视频号直播', 74: '文件发送中', 87: '群公告', 92: '音乐',
        2000: '转账', 2001: '红包', 2003: '红包封面',
    };

    function isMessageRecord(item) {
        if (!item || typeof item !== 'object') return false;
        return item.type_num !== undefined
            || item.message_id !== undefined
            || (item.trigger_type !== undefined && item.trigger_content !== undefined);
    }

    function normalizedMessageType(item) {
        const numeric = Number(item.type_num ?? item.trigger_type ?? item.type);
        return Number.isFinite(numeric) ? numeric : 0;
    }

    function messageTypeMeta(item) {
        const type = normalizedMessageType(item);
        const subtype = Number(item.sub_type ?? item.trigger_sub_type ?? 0);
        if (type === 49 && shareTypeLabels[subtype]) return [shareTypeLabels[subtype], '↗'];
        if (messageTypeLabels[type]) return messageTypeLabels[type];
        const textual = String(item.type || '').trim();
        return [textual && !/^\d+$/.test(textual) ? textual : `类型 ${type || '-'}`, '◇'];
    }

    function appendMessageText(parent, text, empty = '（无文本内容）') {
        const paragraph = document.createElement('p');
        paragraph.className = 'chat-message-text';
        paragraph.textContent = String(text || empty);
        parent.appendChild(paragraph);
        return paragraph;
    }

    function appendMessageLinkCard(parent, title, description, rawURL, icon = '↗') {
        const card = document.createElement('div');
        card.className = 'chat-message-rich-card';
        const mark = document.createElement('span');
        mark.className = 'chat-message-rich-icon';
        mark.textContent = icon;
        const copy = document.createElement('span');
        const strong = document.createElement('strong');
        strong.textContent = String(title || '分享内容');
        const small = document.createElement('small');
        small.textContent = String(description || '');
        copy.append(strong, small);
        card.append(mark, copy);
        const url = String(rawURL || '').trim();
        if (url) {
            try {
                const parsed = new URL(url, location.origin);
                if (parsed.protocol === 'http:' || parsed.protocol === 'https:') {
                    const anchor = document.createElement('a');
                    anchor.href = parsed.href;
                    anchor.target = '_blank';
                    anchor.rel = 'noopener';
                    anchor.textContent = '打开';
                    card.appendChild(anchor);
                }
            } catch (_) {
                // Keep malformed source URLs as raw fields only.
            }
        }
        parent.appendChild(card);
        return card;
    }

    function appendMessageLocationCard(parent, contents, content) {
        parent.classList.add('is-location-card');
        const card = document.createElement('div');
        card.className = 'chat-location-card';
        const title = document.createElement('strong');
        const genericLabel = String(contents.label || '').trim() === '[位置]' ? '' : contents.label;
        title.textContent = String(contents.poiname || genericLabel || contents.cityname || content || '位置消息');
        const coordinates = [contents.x, contents.y].map((item) => String(item || '').trim()).filter(Boolean);
        const summary = document.createElement('small');
        summary.textContent = coordinates.length === 2 ? `坐标 ${coordinates.join(', ')}` : '位置坐标暂缺';
        const map = document.createElement('div');
        map.className = 'chat-location-map';
        const roadA = document.createElement('i');
        roadA.className = 'chat-location-road road-a';
        const roadB = document.createElement('i');
        roadB.className = 'chat-location-road road-b';
        const pin = document.createElement('span');
        pin.className = 'chat-location-pin';
        pin.textContent = '●';
        map.append(roadA, roadB, pin);
        card.append(title, summary, map);
        parent.appendChild(card);
        return card;
    }

    function appendRedEnvelopeCard(parent, contents, subtype) {
        parent.classList.add('is-red-envelope-card');
        const card = document.createElement('div');
        card.className = 'chat-red-envelope-card';
        const main = document.createElement('div');
        main.className = 'chat-red-envelope-main';
        const icon = document.createElement('span');
        icon.className = 'chat-red-envelope-icon';
        icon.textContent = '¥';
        const copy = document.createElement('span');
        const title = document.createElement('strong');
        title.textContent = String(contents.red_envelope_title || '恭喜发财，大吉大利');
        const status = document.createElement('small');
        status.textContent = String(contents.red_envelope_status || contents.description || '领取红包');
        copy.append(title, status);
        main.append(icon, copy);
        const footer = document.createElement('div');
        footer.className = 'chat-red-envelope-footer';
        footer.textContent = String(contents.scene_text || (subtype === 2003 ? '红包封面' : '微信红包'));
        card.append(main, footer);
        parent.appendChild(card);
        return card;
    }

    function appendRedEnvelopeReceipt(parent, content) {
        parent.classList.add('is-red-envelope-receipt');
        const icon = document.createElement('span');
        icon.className = 'chat-red-envelope-receipt-icon';
        icon.textContent = '●';
        const text = document.createElement('span');
        text.textContent = String(content || '红包状态更新');
        parent.append(icon, text);
    }

    function appendMessageMedia(parent, item, preferredType, fallbackLabel) {
        const references = mediaReferences(item).filter((reference) => {
            if (!preferredType) return true;
            return reference.type === preferredType || reference.url.includes(`/${preferredType}/`);
        });
        if (!references.length) {
            appendMessageLinkCard(parent, fallbackLabel, '媒体索引尚未解析或文件当前不在缓存中', '', preferredType === 'voice' ? '◖' : preferredType === 'video' ? '▶' : '▧');
            return false;
        }
        const mediaRow = document.createElement('div');
        mediaRow.className = 'chat-message-media';
        // A chat image/video/voice/file message owns one media object. The API
        // may expose multiple lookup aliases (resolved path, MD5, URL); render
        // one element and use the remaining aliases only after a load failure.
        mediaRow.appendChild(mediaElement(references[0], references.slice(1)));
        parent.appendChild(mediaRow);
        return true;
    }

    function createMessageBody(item) {
        const body = document.createElement('div');
        body.className = 'chat-message-body';
        const type = normalizedMessageType(item);
        const subtype = Number(item.sub_type ?? item.trigger_sub_type ?? 0);
        const contents = (item.contents ?? item.trigger_contents) && typeof (item.contents ?? item.trigger_contents) === 'object'
            ? (item.contents ?? item.trigger_contents) : {};
        const content = item.content ?? item.trigger_content ?? '';

        switch (type) {
        case 1:
            appendMessageText(body, content);
            break;
        case 3:
            appendMessageMedia(body, item, 'image', '图片');
            if (String(contents.image_description || item.image_description || '').trim()) {
                const description = document.createElement('div');
                description.className = 'chat-image-description';
                const label = document.createElement('strong');
                label.textContent = '图片描述';
                const text = document.createElement('span');
                text.textContent = String(contents.image_description || item.image_description).trim();
                description.append(label, text);
                body.appendChild(description);
            }
            break;
        case 34:
            appendMessageMedia(body, item, 'voice', '语音消息');
            break;
        case 42:
            appendMessageLinkCard(body, contents.nickname || content || '联系人名片', contents.alias || contents.username || '', '', '◎');
            break;
        case 43:
            appendMessageMedia(body, item, 'video', '视频');
            break;
        case 47: {
            const stickerURL = String(contents.cdnurl || '').trim();
            appendMessageLinkCard(body, '动画表情', stickerURL ? '已解析表情资源地址' : '表情资源地址暂缺', stickerURL, '☺');
            break;
        }
        case 48: {
            appendMessageLocationCard(body, contents, content);
            break;
        }
        case 49:
            if (subtype === 6) {
                appendMessageLinkCard(body, contents.title || content || '文件', contents.file_ext || '', '', '▤');
                appendMessageMedia(body, item, 'file', '附件');
            } else if (subtype === 57) {
                const refer = contents.refer && typeof contents.refer === 'object' ? contents.refer : null;
                if (refer) {
                    const quote = document.createElement('blockquote');
                    quote.className = 'chat-message-quote';
                    const quoteSender = refer.senderName || refer.sender || '引用消息';
                    quote.textContent = `${quoteSender}：${refer.content || refer.contents?.title || '媒体消息'}`;
                    body.appendChild(quote);
                }
                appendMessageText(body, content, '（引用回复）');
            } else if (subtype === 2000) {
                appendMessageLinkCard(body, content || contents.title || '微信转账', contents.desc || '支付消息', '', '¥');
            } else if (subtype === 2001 || subtype === 2003) {
                appendRedEnvelopeCard(body, contents, subtype);
            } else if (subtype === 19 || subtype === 24 || subtype === 87) {
                const record = contents.recordInfo || {};
                const count = record.DataList?.DataItems?.length || record.dataList?.dataItems?.length || '';
                appendMessageLinkCard(body, contents.title || record.Title || content || shareTypeLabels[subtype], contents.desc || record.Desc || (count ? `${count} 条记录` : ''), contents.url || '', '☰');
            } else {
                appendMessageLinkCard(body, contents.title || content || shareTypeLabels[subtype] || '分享内容', contents.desc || '', contents.url || '', subtype === 33 || subtype === 36 ? '◇' : subtype === 92 ? '♫' : '↗');
            }
            break;
        case 50:
            appendMessageLinkCard(body, '语音通话', content || '通话记录', '', '☎');
            break;
        case 10000:
            if (contents.system_kind === 'red_envelope_receipt') {
                appendRedEnvelopeReceipt(body, content);
            } else {
                appendMessageText(body, content, '系统事件');
            }
            break;
        case 11000:
            appendMessageText(body, content, '系统通知');
            break;
        default:
            appendMessageText(body, content, `（类型 ${type || '-'} 暂无可显示文本）`);
            break;
        }
        return body;
    }

    function createMessageBubble(item, index = 0) {
        const normalized = item && typeof item === 'object' ? item : { content: item };
        const type = normalizedMessageType(normalized);
        const isSelf = normalized.is_self === true || normalized.trigger_is_self === true || normalized.is_self === 1;
        const isSystem = type === 10000;
        const row = document.createElement('article');
        row.className = `chat-message-row${isSelf ? ' is-self' : ''}${isSystem ? ' is-system' : ''}`;
        const sender = String(normalized.sender || normalized.sender_name || normalized.sender_id || (isSelf ? '我' : '未知发送者'));
        if (!isSystem) {
            const avatar = document.createElement('span');
            avatar.className = 'chat-message-avatar';
            avatar.textContent = isSelf ? '我' : Array.from(sender.trim() || '?')[0];
            row.appendChild(avatar);
        }
        const stack = document.createElement('div');
        stack.className = 'chat-message-stack';
        const meta = document.createElement('div');
        meta.className = 'chat-message-meta';
        const [typeLabel, typeIcon] = messageTypeMeta(normalized);
        const chat = String(normalized.chat || normalized.talker_name || normalized.username || normalized.talker || '').trim();
        const time = String(normalized.time || normalized.trigger_time || normalized.timestamp || '').trim();
        const displayTime = /^\d{10,13}$/.test(time)
            ? new Date(Number(time) * (time.length === 10 ? 1000 : 1)).toLocaleString('zh-CN', { hour12: false })
            : time;
        meta.textContent = isSystem ? `${typeIcon} ${typeLabel} · ${displayTime || '-'}` : `${sender}${chat ? ` · ${chat}` : ''} · ${displayTime || '-'}`;
        const badge = document.createElement('span');
        badge.className = 'chat-message-type';
        badge.textContent = `${typeIcon} ${typeLabel}`;
        meta.appendChild(badge);
        stack.append(meta, createMessageBody(normalized));
        const atUsers = Array.isArray(normalized.at_user_list) ? normalized.at_user_list : [];
        if (atUsers.length) {
            const at = document.createElement('small');
            at.className = 'chat-message-at';
            at.textContent = `@ ${atUsers.join('、')}`;
            stack.appendChild(at);
        }
        row.appendChild(stack);
        row.dataset.messageIndex = String(index);
        return row;
    }

    window.chatlogCreateMessagePreview = createMessageBubble;

    function itemTitle(item, index) {
        return item.chat || item.display || item.title || item.from_nickname || item.corp_name
            || item.sender || item.username || item.name || item.type || `记录 ${index + 1}`;
    }

    function itemSummary(item) {
        return item.content || item.summary || item.preview || item.desc || item.description
            || item.remark || item.nickname || item.raw_content || '';
    }

    function isSNSPostRecord(item) {
        if (!item || typeof item !== 'object') return false;
        return item.raw_content?.includes('<TimelineObject')
            || item.raw_content?.includes('<SnsDataItem')
            || (item.content_type !== undefined
                && (item.media_list !== undefined || item.article !== undefined || item.location !== undefined));
    }

    function snsRelativeTime(rawTimestamp, fallback) {
        const timestamp = Number(rawTimestamp);
        if (!Number.isFinite(timestamp) || timestamp <= 0) return String(fallback || '');
        const elapsed = Math.max(0, Date.now() - timestamp * (timestamp < 1e12 ? 1000 : 1));
        const minute = 60 * 1000;
        const hour = 60 * minute;
        const day = 24 * hour;
        if (elapsed < minute) return '刚刚';
        if (elapsed < hour) return `${Math.floor(elapsed / minute)}分钟前`;
        if (elapsed < day) return `${Math.floor(elapsed / hour)}小时前`;
        if (elapsed < 7 * day) return `${Math.floor(elapsed / day)}天前`;
        return new Date(timestamp * (timestamp < 1e12 ? 1000 : 1)).toLocaleDateString('zh-CN');
    }

    function snsMediaTile(media, index) {
        const tile = document.createElement('div');
        const mediaType = String(media?.type || 'image').toLowerCase();
        tile.className = `sns-media-tile is-${mediaType}`;
        const mediaURL = sameOriginMediaURL(media?.proxy_url || media?.resolved_url || media?.url
            || media?.proxy_thumb_url || media?.resolved_thumb_url || media?.thumb);
        if (mediaURL) {
            if (mediaType === 'video') {
                const video = document.createElement('video');
                video.controls = true;
                video.preload = 'metadata';
                video.src = mediaURL;
                tile.appendChild(video);
            } else {
                const image = document.createElement('img');
                image.loading = 'lazy';
                image.alt = `朋友圈图片 ${index + 1}`;
                image.src = mediaURL;
                tile.appendChild(image);
            }
        } else {
            const missing = document.createElement('span');
            missing.className = 'sns-media-missing';
            missing.textContent = mediaType === 'video' ? '视频资源暂缺' : '图片资源暂缺';
            tile.appendChild(missing);
        }
        const liveURL = sameOriginMediaURL(media?.live_photo?.proxy_url
            || media?.live_photo?.resolved_url || media?.live_photo?.url);
        if (liveURL) {
            const live = document.createElement('a');
            live.className = 'sns-live-badge';
            live.href = liveURL;
            live.target = '_blank';
            live.rel = 'noopener';
            live.textContent = '◉ 实况';
            live.title = '打开实况照片视频';
            tile.appendChild(live);
        }
        return tile;
    }

    function appendSNSArticle(parent, article) {
        if (!article || typeof article !== 'object') return;
        const rawURL = String(article.url || '').trim();
        const card = document.createElement(absoluteHTTPURL(rawURL) ? 'a' : 'div');
        card.className = 'sns-article-card';
        if (card.tagName === 'A') {
            card.href = rawURL;
            card.target = '_blank';
            card.rel = 'noopener';
        }
        const coverURL = sameOriginMediaURL(article.proxy_cover_url || article.cover_url);
        if (coverURL) {
            const cover = document.createElement('img');
            cover.className = 'sns-article-cover';
            cover.loading = 'lazy';
            cover.alt = article.title || '文章封面';
            cover.src = coverURL;
            card.appendChild(cover);
        } else {
            const cover = document.createElement('span');
            cover.className = 'sns-article-cover is-placeholder';
            cover.textContent = '文章';
            card.appendChild(cover);
        }
        const copy = document.createElement('span');
        copy.className = 'sns-article-copy';
        const title = document.createElement('strong');
        title.textContent = String(article.title || '公众号文章');
        copy.appendChild(title);
        if (article.description) {
            const description = document.createElement('small');
            description.textContent = String(article.description);
            copy.appendChild(description);
        }
        if (article.source_name) {
            const source = document.createElement('em');
            source.textContent = `公众号 · ${article.source_name}`;
            copy.appendChild(source);
        }
        card.appendChild(copy);
        parent.appendChild(card);
    }

    function appendSNSInteractions(parent, item) {
        const likes = Array.isArray(item.likes) ? item.likes : [];
        const comments = Array.isArray(item.comments) ? item.comments : [];
        if (!likes.length && !comments.length) return;
        const interactions = document.createElement('div');
        interactions.className = 'sns-interactions';
        if (likes.length) {
            const likeRow = document.createElement('div');
            likeRow.className = 'sns-like-row';
            const icon = document.createElement('span');
            icon.textContent = '♡';
            const names = document.createElement('strong');
            names.textContent = likes.map((like) => like.nickname || like.username).filter(Boolean).join('、');
            likeRow.append(icon, names);
            interactions.appendChild(likeRow);
        }
        if (comments.length) {
            const commentList = document.createElement('div');
            commentList.className = 'sns-comment-list';
            comments.forEach((comment) => {
                const row = document.createElement('div');
                row.className = 'sns-comment';
                const author = document.createElement('strong');
                const nickname = String(comment.nickname || comment.username || '未知用户');
                const reply = String(comment.reply_to_nickname || comment.reply_to_username || '');
                author.textContent = reply ? `${nickname} 回复 ${reply}：` : `${nickname}：`;
                const content = document.createElement('span');
                content.textContent = String(comment.content || '');
                row.append(author, content);
                commentList.appendChild(row);
            });
            interactions.appendChild(commentList);
        }
        parent.appendChild(interactions);
    }

    function createSNSPostCard(item, index = 0) {
        const card = document.createElement('article');
        card.className = 'sns-post-card';
        const avatar = document.createElement('span');
        avatar.className = 'sns-post-avatar';
        const authorName = String(item.display || item.nickname || item.username || `动态 ${index + 1}`);
        const avatarURL = sameOriginMediaURL(item.avatar_url);
        if (avatarURL) {
            const image = document.createElement('img');
            image.loading = 'lazy';
            image.alt = `${authorName}头像`;
            image.src = avatarURL;
            avatar.appendChild(image);
        } else {
            avatar.textContent = Array.from(authorName.trim() || '?')[0];
        }
        const body = document.createElement('div');
        body.className = 'sns-post-body';
        const header = document.createElement('div');
        header.className = 'sns-post-header';
        const author = document.createElement('strong');
        author.className = 'sns-post-author';
        author.textContent = authorName;
        const time = document.createElement('time');
        time.className = 'sns-post-time';
        time.textContent = snsRelativeTime(item.timestamp, item.time);
        header.append(author, time);
        body.appendChild(header);
        const content = String(item.content || '').trim();
        if (content) {
            const paragraph = document.createElement('p');
            paragraph.className = 'sns-post-content';
            paragraph.textContent = content;
            body.appendChild(paragraph);
        }
        appendSNSArticle(body, item.article);
        const mediaList = Array.isArray(item.media_list) ? item.media_list : [];
        if (mediaList.length) {
            const mediaGrid = document.createElement('div');
            const countClass = mediaList.length === 1 ? 'is-single' : mediaList.length === 2 ? 'is-pair' : 'is-grid';
            mediaGrid.className = `sns-media-grid ${countClass}`;
            mediaList.slice(0, 9).forEach((media, mediaIndex) => {
                mediaGrid.appendChild(snsMediaTile(media, mediaIndex));
            });
            body.appendChild(mediaGrid);
        }
        if (item.location && typeof item.location === 'object') {
            const locationName = item.location.poi_name || item.location.city || item.location.poi_address;
            if (locationName) {
                const location = document.createElement('div');
                location.className = 'sns-location';
                location.textContent = `⌖ ${locationName}`;
                if (item.location.city && item.location.poi_name && !String(item.location.poi_name).includes(item.location.city)) {
                    location.title = `${item.location.city} · ${item.location.poi_name}`;
                }
                body.appendChild(location);
            }
        }
        appendSNSInteractions(body, item);
        const details = document.createElement('details');
        details.className = 'structured-raw sns-raw-fields';
        const detailsSummary = document.createElement('summary');
        detailsSummary.textContent = '解析字段';
        const pre = document.createElement('pre');
        pre.textContent = JSON.stringify(item, null, 2);
        details.append(detailsSummary, pre);
        body.appendChild(details);
        card.append(avatar, body);
        card.dataset.snsIndex = String(index);
        return card;
    }

    function structuredCards(data, source = '') {
        const items = extractCollection(data);
        if (!items.length) return null;
        const shell = document.createElement('div');
        shell.className = 'structured-card-shell';
        const summary = document.createElement('div');
        summary.className = 'structured-summary';
        const total = data.total_count ?? data.total ?? data.count ?? items.length;
        summary.textContent = `${source ? `${source} · ` : ''}本次 ${items.length} 条${Number(total) !== items.length ? ` / 总计 ${total}` : ''}`;
        shell.appendChild(summary);
        const grid = document.createElement('div');
        const messageCollection = items.every((item) => isMessageRecord(item));
        const snsCollection = items.every((item) => isSNSPostRecord(item));
        grid.className = messageCollection ? 'chat-preview-list'
            : snsCollection ? 'sns-feed-list' : 'structured-card-grid';
        items.slice(0, 200).forEach((item, index) => {
            const normalized = item && typeof item === 'object' ? item : { value: item };
            if (messageCollection) {
                grid.appendChild(createMessageBubble(normalized, index));
                return;
            }
            if (snsCollection) {
                grid.appendChild(createSNSPostCard(normalized, index));
                return;
            }
            const card = document.createElement('article');
            card.className = 'structured-card';
            const head = document.createElement('div');
            head.className = 'structured-card-head';
            const strong = document.createElement('strong');
            strong.textContent = String(itemTitle(normalized, index));
            const meta = document.createElement('span');
            meta.textContent = String(normalized.time || normalized.timestamp || normalized.chat_type || normalized.type || '');
            head.append(strong, meta);
            card.appendChild(head);
            const content = itemSummary(normalized);
            if (content) {
                const paragraph = document.createElement('p');
                paragraph.textContent = String(content);
                card.appendChild(paragraph);
            }
            const facts = [
                normalized.username && `ID: ${normalized.username}`,
                normalized.corp_id && `企业 ID: ${normalized.corp_id}`,
                normalized.unread != null && `未读: ${normalized.unread}`,
                normalized.user_count != null && `成员: ${normalized.user_count}`,
                normalized.is_owner && '群主',
            ].filter(Boolean);
            if (facts.length) {
                const factRow = document.createElement('div');
                factRow.className = 'structured-facts';
                facts.forEach((fact) => {
                    const badge = document.createElement('span');
                    badge.textContent = fact;
                    factRow.appendChild(badge);
                });
                card.appendChild(factRow);
            }
            const references = mediaReferences(normalized);
            if (references.length) {
                const mediaRow = document.createElement('div');
                mediaRow.className = 'inline-media-grid';
                references.forEach((reference) => mediaRow.appendChild(mediaElement(reference)));
                card.appendChild(mediaRow);
            }
            const details = document.createElement('details');
            details.className = 'structured-raw';
            const detailsSummary = document.createElement('summary');
            detailsSummary.textContent = '原始字段';
            const pre = document.createElement('pre');
            pre.textContent = JSON.stringify(normalized, null, 2);
            details.append(detailsSummary, pre);
            card.appendChild(details);
            grid.appendChild(card);
        });
        shell.appendChild(grid);
        if (items.length > 200) {
            const notice = document.createElement('div');
            notice.className = 'analytics-empty';
            notice.textContent = `为避免浏览器卡顿，仅渲染前 200 条；原始 JSON 仍完整保留。`;
            shell.appendChild(notice);
        }
        return shell;
    }

    function renderStructured(containerID, data, source = '') {
        const container = byID(containerID);
        if (!container) return;
        container.replaceChildren();
        const cards = structuredCards(data, source);
        if (cards) container.appendChild(cards);
        else {
            const empty = document.createElement('div');
            empty.className = 'analytics-empty';
            empty.textContent = '没有匹配记录';
            container.appendChild(empty);
        }
        const details = document.createElement('details');
        details.className = 'structured-response';
        const summary = document.createElement('summary');
        summary.textContent = '查看完整 JSON';
        const pre = document.createElement('pre');
        pre.textContent = JSON.stringify(data, null, 2);
        details.append(summary, pre);
        container.appendChild(details);
    }

    window.chatlogEnhanceStructuredResult = (container, data, source, rawPre) => {
        const cards = structuredCards(data, source);
        if (cards) container.insertBefore(cards, rawPre || container.firstChild);
    };

    function updateMessageCenterState(text, error = false) {
        const state = byID('message-center-state');
        if (!state) return;
        state.textContent = text;
        state.classList.toggle('is-error', error);
    }

    window.messageCenterSearch = async (useCursor = false) => {
        const keyword = value('mc-search-keyword');
        if (!keyword) {
            showError('mc-search-result', '关键词不能为空');
            return;
        }
        const messageType = optionalInteger('mc-search-msg-type', 0);
        if (!messageType.valid) {
            showError('mc-search-result', '消息类型必须是非负整数');
            return;
        }
        const params = new URLSearchParams({ keyword });
        appendParam(params, 'chats', value('mc-search-chats'));
        appendParam(params, 'time', value('mc-search-time'));
        appendParam(params, 'msg_type', messageType.value);
        appendParam(params, 'match', value('mc-search-match'));
        appendParam(params, 'sort', value('mc-search-sort'));
        params.set('limit', String(numberValue('mc-search-limit', 50, 1)));
        if (useCursor && messageSearchNextCursor) {
            params.set('cursor', messageSearchNextCursor);
            params.set('offset', '0');
        } else {
            params.set('offset', String(numberValue('mc-search-offset', 0, 0)));
        }
        setBusy('mc-search-result', '正在全文搜索…');
        updateMessageCenterState('搜索中');
        try {
            const data = await requestJSON(`/api/v1/search?${params}`);
            messageSearchNextCursor = String(data.next_cursor || '');
            renderStructured('mc-search-result', data, '全文搜索');
            updateMessageCenterState(`搜索完成 · ${data.count ?? data.messages?.length ?? 0} 条`);
        } catch (error) {
            showError('mc-search-result', error);
            updateMessageCenterState('搜索失败', true);
        }
    };

    window.messageCenterSearchPage = (direction) => {
        messageSearchNextCursor = '';
        const limit = numberValue('mc-search-limit', 50, 1);
        const offsetInput = byID('mc-search-offset');
        offsetInput.value = String(Math.max(0, numberValue('mc-search-offset', 0, 0) + direction * limit));
        window.messageCenterSearch();
    };

    window.messageCenterSearchNextCursor = () => {
        if (!messageSearchNextCursor) {
            showError('mc-search-result', '当前结果没有后续游标');
            return;
        }
        window.messageCenterSearch(true);
    };

    window.messageCenterLoadUnread = async () => {
        const params = new URLSearchParams();
        appendParam(params, 'filter', value('mc-unread-filter'));
        params.set('limit', String(numberValue('mc-unread-limit', 20, 1)));
        setBusy('mc-unread-result', '正在读取未读会话…');
        try {
            const data = await requestJSON(`/api/v1/unread?${params}`);
            renderStructured('mc-unread-result', data, '未读会话');
        } catch (error) {
            showError('mc-unread-result', error);
        }
    };

    window.messageCenterLoadNew = async () => {
        if (newMessageRequest) return newMessageRequest;
        const params = new URLSearchParams();
        params.set('limit', String(numberValue('mc-new-limit', 100, 1)));
        const headers = {};
        if (Object.keys(newMessageState).length) headers['X-Chatlog-State'] = JSON.stringify(newMessageState);
        setBusy('mc-new-result', '正在读取增量消息…');
        newMessageRequest = (async () => {
            try {
                const data = await requestJSON(`/api/v1/new_messages?${params}`, { headers });
                if (data.new_state && typeof data.new_state === 'object') newMessageState = data.new_state;
                renderStructured('mc-new-result', data, data.has_more ? '增量消息（仍有待读取）' : '增量消息');
                return data;
            } catch (error) {
                showError('mc-new-result', error);
                return null;
            } finally {
                newMessageRequest = null;
            }
        })();
        return newMessageRequest;
    };

    window.messageCenterTogglePolling = () => {
        if (!byID('mc-new-auto')?.checked) return;
        window.messageCenterLoadNew();
    };

    window.messageCenterResetState = () => {
        newMessageState = {};
        const container = byID('mc-new-result');
        if (container) container.innerHTML = '<div class="analytics-empty">游标已重置，下次读取最近 24 小时内的新消息。</div>';
    };

    window.messageCenterLoadMembers = async () => {
        const chat = value('mc-members-chat');
        if (!chat) {
            showError('mc-members-result', '群聊 ID 不能为空');
            return;
        }
        if (!chat.toLowerCase().endsWith('@chatroom')) {
            showError('mc-members-result', '群聊 ID 必须以 @chatroom 结尾');
            return;
        }
        setBusy('mc-members-result', '正在读取群成员…');
        try {
            const data = await requestJSON(`/api/v1/members?chat=${encodeURIComponent(chat)}`);
            renderStructured('mc-members-result', data, '群成员');
        } catch (error) {
            showError('mc-members-result', error);
        }
    };

    const ocrLocalEndpoint = 'http://127.0.0.1:8081/glmocr/parse';
    const ocrAPIEndpoint = 'https://open.bigmodel.cn/api/paas/v4/layout_parsing';

    function ocrProviderLabel(provider, mode = '') {
        if (mode === 'api' || provider === 'maas') return '智谱 GLM-OCR API';
        if (provider === 'vllm') return '本地 GLM-OCR SDK';
        return provider || '待分配';
    }

    function ocrModeValue() {
        return document.querySelector('input[name="ocr-mode"]:checked')?.value || 'local';
    }

    function renderOCRModeExplainer(mode) {
        const explainer = byID('ocr-mode-explainer');
        if (!explainer) return;
        explainer.textContent = mode === 'api'
            ? 'API 模式会将图片发送至智谱 GLM-OCR MaaS；保存前请填写 API Key。未完成任务会立即切换到 API 调用。'
            : '本地模式统一调用 GLM-OCR SDK。Apple Silicon 使用 MLX 自动托管，NVIDIA 主机使用 vLLM；未完成任务会立即切换到本地服务。';
    }

    window.ocrCenterDeploymentGuide = (platform) => {
        const selected = platform === 'vllm' ? 'vllm' : 'mlx';
        ['mlx', 'vllm'].forEach((name) => {
            byID(`ocr-deploy-tab-${name}`)?.classList.toggle('is-active', name === selected);
            byID(`ocr-deploy-guide-${name}`)?.classList.toggle('is-active', name === selected);
        });
    };

    window.ocrCenterCopyDeployment = async (platform) => {
        const selected = platform === 'vllm' ? 'vllm' : 'mlx';
        const command = byID(`ocr-deploy-${selected}-command`)?.textContent?.trim();
        if (!command) return;
        try {
            await navigator.clipboard.writeText(command);
            showToast(`${selected === 'mlx' ? 'MLX' : 'vLLM'} 部署命令已复制`);
        } catch (_) {
            showToast('复制失败，请从命令框手动复制', 'error');
        }
    };

    function formatOCRTime(raw) {
        const value = Number(raw || 0);
        if (!value) return '-';
        return new Date(value * 1000).toLocaleString('zh-CN', { hour12: false });
    }

    function setOCRText(id, text, title = '') {
        const node = byID(id);
        if (!node) return;
        node.textContent = String(text ?? '-');
        node.title = String(title || '');
    }

    function ocrCenterSelectedValues(type) {
        return Array.from(type === 'contact' ? ocrSelectedContacts : ocrSelectedChatRooms);
    }

    function updateOCRScopeCounts() {
        setOCRText('ocr-contact-count', `${ocrSelectedContacts.size} 已选`);
        setOCRText('ocr-chatroom-count', `${ocrSelectedChatRooms.size} 已选`);
    }

    function renderOCRScopeOptions(type) {
        const contacts = type === 'contact';
        const container = byID(contacts ? 'ocr-contact-options' : 'ocr-chatroom-options');
        if (!container) return;
        const collection = contacts ? ocrContacts : ocrChatRooms;
        const selected = contacts ? ocrSelectedContacts : ocrSelectedChatRooms;
        const filter = value(contacts ? 'ocr-contact-filter' : 'ocr-chatroom-filter').toLowerCase();
        const identifierOf = (item) => String(contacts ? item?.username : item?.name || '').trim();
        const known = new Set(collection.map(identifierOf).filter(Boolean));
        const candidates = collection.map((item, index) => ({ item, index, identifier: identifierOf(item) }));
        selected.forEach((identifier) => {
            if (!known.has(identifier)) {
                candidates.push({
                    item: contacts
                        ? { username: identifier, display: identifier, missing: true }
                        : { name: identifier, display: identifier, missing: true },
                    index: collection.length + candidates.length,
                    identifier,
                });
            }
        });
        const visible = candidates.filter(({ item, identifier }) => {
            const text = contacts
                ? `${item.display || ''} ${item.remark || ''} ${item.nickname || ''} ${item.username || ''}`
                : `${item.display || ''} ${item.remark || ''} ${item.nickname || ''} ${item.name || ''}`;
            return identifier && (!filter || text.toLowerCase().includes(filter));
        }).sort((left, right) => {
            const leftSelected = selected.has(left.identifier) ? 0 : 1;
            const rightSelected = selected.has(right.identifier) ? 0 : 1;
            return leftSelected - rightSelected || left.index - right.index;
        });
        container.replaceChildren();
        if (!visible.length) {
            container.innerHTML = '<div class="analytics-empty">没有匹配项目</div>';
            return;
        }
        visible.forEach(({ item, identifier }) => {
            const label = document.createElement('label');
            label.className = 'ocr-option-item';
            label.classList.toggle('is-selected', selected.has(identifier));
            const checkbox = document.createElement('input');
            checkbox.type = 'checkbox';
            checkbox.checked = selected.has(identifier);
            checkbox.addEventListener('change', () => {
                if (checkbox.checked) selected.add(identifier);
                else selected.delete(identifier);
                updateOCRScopeCounts();
                renderOCRScopeOptions(type);
            });
            const text = document.createElement('span');
            const strong = document.createElement('strong');
            strong.textContent = String(item.display || identifier);
            const small = document.createElement('small');
            small.textContent = contacts
                ? `${identifier}${item.remark ? ` · ${item.remark}` : ''}${item.missing ? ' · 已选但当前目录未返回' : ''}`
                : `${identifier}${item.user_count !== undefined ? ` · ${item.user_count} 人` : ''}${item.missing ? ' · 已选但当前目录未返回' : ''}`;
            text.append(strong, small);
            label.append(checkbox, text);
            container.appendChild(label);
        });
    }

    window.ocrCenterFilterScope = (type) => renderOCRScopeOptions(type);

    window.ocrCenterScopeModeChanged = () => {
        byID('ocr-scope-selected')?.classList.toggle('hidden', value('ocr-scope-mode') !== 'selected');
        syncOCRPowerUI();
    };

    window.ocrCenterModeChanged = () => {
        const endpoint = byID('ocr-endpoint');
        if (!endpoint) return;
        const mode = ocrModeValue();
        if (ocrLastMode !== mode) {
            const current = endpoint.value.trim();
            if (current) ocrEndpointByMode[ocrLastMode] = current;
            endpoint.value = ocrEndpointByMode[mode] ||
                (mode === 'api' ? ocrAPIEndpoint : ocrLocalEndpoint);
            ocrLastMode = mode;
        } else if (!endpoint.value.trim()) {
            endpoint.value = ocrEndpointByMode[mode] ||
                (mode === 'api' ? ocrAPIEndpoint : ocrLocalEndpoint);
        }
        byID('ocr-api-key')?.closest('.form-group')?.classList.toggle('is-muted', mode !== 'api');
        renderOCRModeExplainer(mode);
        setOCRText(
            'ocr-route-title',
            mode === 'api' ? '智谱 API → OCR 索引' : 'GLM-OCR SDK → 本地推理 → OCR 索引',
        );
        setOCRText(
            'ocr-route-detail',
            mode === 'api'
                ? '保存后，未完成任务将使用 MaaS API 重新排队'
                : '保存后，未完成任务将使用 MLX/vLLM 本地服务重新排队',
        );
        setOCRText('ocr-route-service', mode === 'api' ? '智谱 MaaS' : '本地 SDK');
    };

    let ocrAutoDownloadAvailable = false;

    function syncOCRPowerUI() {
        const realtimeOn = Boolean(byID('ocr-realtime-enabled')?.checked);
        const backfillOn = Boolean(byID('ocr-backfill-enabled')?.checked);
        const autoDownloadOn = Boolean(byID('ocr-auto-download-enabled')?.checked);
        const selectedScope = value('ocr-scope-mode') === 'selected';
        const realtimeCard = document.querySelector('[data-ocr-power="realtime"]');
        const backfillCard = document.querySelector('[data-ocr-power="backfill"]');
        const autoDownloadCard = document.querySelector('[data-ocr-power="auto-download"]');
        realtimeCard?.classList.toggle('is-on', realtimeOn);
        backfillCard?.classList.toggle('is-on', backfillOn);
        autoDownloadCard?.classList.toggle('is-on', autoDownloadOn);
        autoDownloadCard?.classList.toggle('is-unavailable', !ocrAutoDownloadAvailable);
        const realtimeState = byID('ocr-realtime-power-state');
        if (realtimeState) {
            realtimeState.textContent = realtimeOn ? '已打开' : '已关闭';
            realtimeState.classList.toggle('is-on', realtimeOn);
            realtimeState.classList.toggle('is-off', !realtimeOn);
        }
        const backfillState = byID('ocr-backfill-power-state');
        if (backfillState) {
            backfillState.textContent = backfillOn ? '已打开' : '已关闭';
            backfillState.classList.toggle('is-on', backfillOn);
            backfillState.classList.toggle('is-off', !backfillOn);
        }
        const autoDownloadState = byID('ocr-auto-download-power-state');
        if (autoDownloadState) {
            autoDownloadState.textContent = autoDownloadOn
                ? (selectedScope ? '已打开 · 名单模式' : '已打开')
                : '已关闭';
            autoDownloadState.classList.toggle('is-on', autoDownloadOn);
            autoDownloadState.classList.toggle('is-off', !autoDownloadOn);
        }
        const availability = byID('ocr-auto-download-availability');
        if (availability) {
            availability.textContent = ocrAutoDownloadAvailable
                ? (selectedScope
                    ? '名单内图片精准升级，其他会话保持微信原生缩略图请求'
                    : '本机已检测到 Frida，可在图片接收时自动落盘普通图片')
                : '本机未检测到可用 Frida，接收升级功能未就绪';
        }
        byID('ocr-realtime-open')?.classList.toggle('is-active', realtimeOn);
        byID('ocr-realtime-close')?.classList.toggle('is-active', !realtimeOn);
        byID('ocr-backfill-open')?.classList.toggle('is-active', backfillOn);
        byID('ocr-backfill-close')?.classList.toggle('is-active', !backfillOn);
        byID('ocr-auto-download-open')?.classList.toggle('is-active', autoDownloadOn);
        byID('ocr-auto-download-close')?.classList.toggle('is-active', !autoDownloadOn);
        byID('ocr-realtime-open') && (byID('ocr-realtime-open').disabled = realtimeOn);
        byID('ocr-realtime-close') && (byID('ocr-realtime-close').disabled = !realtimeOn);
        byID('ocr-backfill-open') && (byID('ocr-backfill-open').disabled = backfillOn);
        byID('ocr-backfill-close') && (byID('ocr-backfill-close').disabled = !backfillOn);
        byID('ocr-auto-download-open') && (byID('ocr-auto-download-open').disabled = autoDownloadOn);
        byID('ocr-auto-download-close') && (byID('ocr-auto-download-close').disabled = !autoDownloadOn);
        window.ocrCenterBackfillToggleChanged();
    }

    window.ocrCenterBackfillToggleChanged = () => {
        const running = byID('ocr-backfill-stop')?.disabled === false;
        if (byID('ocr-backfill-start')) {
            byID('ocr-backfill-start').disabled = !byID('ocr-backfill-enabled')?.checked || running;
        }
    };

    window.ocrCenterSetPower = async (kind, enabled) => {
        let target = byID('ocr-realtime-enabled');
        let label = '实时 OCR';
        if (kind === 'backfill') {
            target = byID('ocr-backfill-enabled');
            label = '历史补录';
        } else if (kind === 'auto-download') {
            target = byID('ocr-auto-download-enabled');
            label = '自动触发原图下载';
            if (Boolean(enabled) && !ocrAutoDownloadAvailable) {
                setOCRText('ocr-page-state', '本机无可用 Frida，无法打开自动触发');
                return;
            }
        }
        if (!target) return;
        const next = Boolean(enabled);
        if (Boolean(target.checked) === next) {
            syncOCRPowerUI();
            return;
        }
        target.checked = next;
        syncOCRPowerUI();
        setOCRText('ocr-page-state', next ? `正在打开${label}…` : `正在关闭${label}…`);
        const ok = await window.ocrCenterSaveConfig({ silent: true });
        if (!ok) {
            target.checked = !next;
            syncOCRPowerUI();
            return;
        }
        setOCRText('ocr-page-state', next ? `${label}已打开` : `${label}已关闭`);
    };

    async function loadOCRScopeOptions() {
        try {
            const [contacts, chatrooms] = await Promise.all([
                requestJSON('/api/v1/contacts?limit=1000&offset=0&is_friend=true'),
                requestJSON('/api/v1/chatrooms?limit=1000&offset=0'),
            ]);
            ocrContacts = Array.isArray(contacts.contacts)
                ? contacts.contacts.filter((item) => !String(item?.username || '').toLowerCase().endsWith('@chatroom'))
                : [];
            ocrChatRooms = Array.isArray(chatrooms.chatrooms) ? chatrooms.chatrooms : [];
            renderOCRScopeOptions('contact');
            renderOCRScopeOptions('chatroom');
        } catch (error) {
            showError('ocr-contact-options', error);
            showError('ocr-chatroom-options', error);
        }
    }

    async function loadOCRConfig() {
        const data = await requestJSON('/api/v1/ocr/config');
        byID('ocr-realtime-enabled').checked = Boolean(data.enabled);
        byID('ocr-backfill-enabled').checked = Boolean(data.backfill_enabled);
        ocrAutoDownloadAvailable = Boolean(data.auto_download_available);
        if (byID('ocr-auto-download-enabled')) {
            byID('ocr-auto-download-enabled').checked = Boolean(data.auto_download_original);
        }
        syncOCRPowerUI();
        if (byID('ocr-local-auto-start')) byID('ocr-local-auto-start').checked = data.local_auto_start !== false;
        if (byID('ocr-local-auto-restart')) byID('ocr-local-auto-restart').checked = data.local_auto_restart !== false;
        const mode = data.mode === 'api' ? 'api' : 'local';
        const radio = document.querySelector(`input[name="ocr-mode"][value="${mode}"]`);
        if (radio) radio.checked = true;
        byID('ocr-endpoint').value = data.endpoint || (mode === 'api' ? ocrAPIEndpoint : ocrLocalEndpoint);
        ocrLastMode = mode;
        ocrEndpointByMode[mode] = byID('ocr-endpoint').value;
        byID('ocr-model').value = data.model || 'glm-ocr';
        byID('ocr-poll-interval').value = String(data.poll_interval_sec || 5);
        byID('ocr-request-timeout').value = String(data.request_timeout_sec || 180);
        byID('ocr-received-only').checked = data.received_only !== false;
        byID('ocr-backfill-received-only').checked = data.received_only !== false;
        byID('ocr-scope-mode').value = data.scope_all === false ? 'selected' : 'all';
        ocrSelectedContacts = new Set(Array.isArray(data.listen_contacts) ? data.listen_contacts : []);
        ocrSelectedChatRooms = new Set(Array.isArray(data.listen_chatrooms) ? data.listen_chatrooms : []);
        const keyInput = byID('ocr-api-key');
        if (keyInput) {
            keyInput.value = '';
            keyInput.placeholder = data.api_key_configured ? '已配置；留空保持当前值' : '输入智谱 API Key';
        }
        byID('ocr-clear-api-key').checked = false;
        const overrides = Array.isArray(data.environment_overrides) ? data.environment_overrides : [];
        const note = byID('ocr-environment-note');
        if (note) {
            note.classList.toggle('hidden', !overrides.length);
            note.textContent = overrides.length
                ? `以下环境变量具有更高优先级：${overrides.join('、')}`
                : '';
        }
        window.ocrCenterModeChanged();
        window.ocrCenterScopeModeChanged();
        syncOCRPowerUI();
        updateOCRScopeCounts();
        renderOCRScopeOptions('contact');
        renderOCRScopeOptions('chatroom');
        ocrConfigLoaded = true;
        return data;
    }

    function renderOCRTaskList(records) {
        const container = byID('ocr-task-list');
        if (!container) return;
        container.replaceChildren();
        if (!Array.isArray(records) || !records.length) {
            container.innerHTML = '<div class="analytics-empty">暂无处理记录</div>';
            return;
        }
        records.forEach((record) => {
            const item = document.createElement('article');
            item.className = `ocr-task-row is-${String(record.status || 'unknown')}`;
            item.tabIndex = 0;
            item.setAttribute('role', 'button');
            item.setAttribute('aria-label', `查看 OCR 记录 #${record.id} 完整内容`);
            item.addEventListener('click', () => window.ocrCenterOpenDetail(record.id));
            item.addEventListener('keydown', (event) => {
                if (event.key === 'Enter' || event.key === ' ') {
                    event.preventDefault();
                    window.ocrCenterOpenDetail(record.id);
                }
            });
            const status = document.createElement('span');
            status.className = 'ocr-task-status';
            status.textContent = {
                waiting_media: '等待中图',
                pending: '等待',
                processing: '处理中',
                succeeded: '成功',
                failed: '失败',
            }[record.status] || record.status || '-';
            const summary = document.createElement('div');
            summary.className = 'ocr-task-summary';
            const title = document.createElement('strong');
            title.textContent = record.talker_name || record.talker || `记录 ${record.id}`;
            const meta = document.createElement('small');
            const targetLabel = ocrProviderLabel(record.provider);
            const mediaRetryCount = Number(record.media_retry_attempts || 0);
            const mediaRetrySuffix = record.status === 'waiting_media'
                ? ` · 下载重试 ${mediaRetryCount} 次${record.media_next_retry_at ? ` · 下次 ${formatOCRTime(record.media_next_retry_at)}` : ''}`
                : '';
            meta.textContent = `#${record.id} · ${formatOCRTime(record.message_time)} · OCR 尝试 ${record.attempts || 0} 次${mediaRetrySuffix} · ${record.status === 'succeeded' ? '识别来源' : '重试目标'}：${targetLabel}`;
            const detail = document.createElement('p');
            detail.textContent = record.media_retry_error || record.error || record.description || (record.status === 'waiting_media' ? '仅有缩略图，尚未进入 OCR 识别队列' : '等待处理结果');
            summary.append(title, meta, detail);
            const action = document.createElement('div');
            action.className = 'ocr-task-action';
            action.addEventListener('click', (event) => event.stopPropagation());
            action.addEventListener('keydown', (event) => event.stopPropagation());
            if (record.status === 'waiting_media') {
                const retryMedia = document.createElement('button');
                retryMedia.type = 'button';
                retryMedia.className = 'btn btn-secondary btn-sm';
                retryMedia.textContent = '重试下载';
                retryMedia.title = '重新检查本地中图，并向微信提交该消息的原生下载请求';
                retryMedia.addEventListener('click', () => window.ocrCenterRetryWaitingMedia(record.id));
                action.appendChild(retryMedia);
            } else if (record.status === 'failed') {
                const retry = document.createElement('button');
                retry.type = 'button';
                retry.className = 'btn btn-secondary btn-sm';
                retry.textContent = '立即重试';
                retry.addEventListener('click', () => window.ocrCenterRetry(record.id));
                action.appendChild(retry);
            } else if (record.status === 'succeeded') {
                const rerecognize = document.createElement('button');
                rerecognize.type = 'button';
                rerecognize.className = 'btn btn-secondary btn-sm';
                rerecognize.textContent = '重新识别';
                rerecognize.title = '清除当前识别结果，并使用现有模式和模型重新处理这张图片';
                rerecognize.addEventListener('click', () => window.ocrCenterRerecognize(record.id));
                action.appendChild(rerecognize);
            }
            item.append(status, summary, action);
            container.appendChild(item);
        });
    }

    function ocrProcessStageLabel(stage) {
        return ({
            loading: '定位图片',
            download: '原图下载',
            recognizing: 'OCR 识别',
            saving: '写入结果',
            idle: '空闲',
            checking: '检查本地',
            triggering: '触发下载',
            waiting: '等待落盘',
            arming: '正在挂载',
            armed: '接收 Hook 已就绪',
            scoped_arming: '正在挂载名单 Hook',
            scoped_armed: '名单 Hook 已就绪',
            retry_armed: '下载重试已预授权',
            unavailable: 'Hook 未就绪',
            ready: '下载成功',
            local: '本地已有',
            skipped: '已跳过',
            error: '下载失败',
        }[stage] || stage || '空闲');
    }

    function ocrShortError(text, maxLen) {
        const raw = String(text || '').trim();
        if (!raw) return '';
        const limit = maxLen || 120;
        // Prefer the humanized leading sentence; drop giant JSON blobs.
        let clean = raw;
        if (clean.includes('{"ok"') || clean.includes('"capture"')) {
            const m = clean.match(/no_manager_captured[^"]*|未捕获微信 CDN[^"]*|原图下载[^"]{0,160}/);
            if (m && m[0]) clean = m[0];
            else clean = clean.split('{')[0].trim() || '原图下载失败（详见技术信息）';
        }
        if (clean.length > limit) return `${clean.slice(0, limit)}…`;
        return clean;
    }

    function renderOCRProcessStatus(data) {
        const panel = byID('ocr-process-status');
        if (!panel) return;
        const current = data.current || {};
        const download = data.auto_download || {};
        const stage = current.stage || (current.record_id ? 'loading' : (download.active ? download.stage : 'idle'));
        const stageLabel = ocrProcessStageLabel(stage);
        const detailParts = [];
        if (current.detail) detailParts.push(ocrShortError(current.detail, 160));
        if (download.detail && download.detail !== current.detail) {
            detailParts.push(`原图：${ocrShortError(download.detail, 160)}`);
        }
        if (!detailParts.length) {
            detailParts.push(current.record_id ? '任务处理中…' : '等待任务…');
        }
        const stageEl = byID('ocr-process-stage');
        if (stageEl) {
            stageEl.textContent = stageLabel;
            stageEl.classList.toggle('is-on', Boolean(current.record_id || download.active));
            stageEl.classList.toggle('is-off', !(current.record_id || download.active));
        }
        setOCRText('ocr-process-detail', detailParts.join(' · '));
        const taskText = current.record_id
            ? `任务：#${current.record_id} · ${current.talker || '-'} · ${current.media_key || '-'}${current.started_at ? ` · 开始于 ${formatOCRTime(current.started_at)}` : ''}`
            : '任务：当前无处理中任务';
        setOCRText('ocr-process-task', taskText);
        let downloadText = '原图下载：未开启';
        if (download.enabled) {
            if (!download.available) {
                downloadText = '原图下载：已开启，但本机 Frida 不可用';
            } else if (download.active) {
                const elapsed = download.started_at
                    ? Math.max(0, Math.floor(Date.now() / 1000 - Number(download.started_at)))
                    : 0;
                downloadText = `原图下载：进行中 · ${ocrProcessStageLabel(download.stage)} · ${download.media_key || '-'}${elapsed ? ` · 已 ${elapsed}s` : ''}`;
            } else if (download.stage) {
                downloadText = `原图下载：${ocrProcessStageLabel(download.stage)} · 触发 ${Number(download.triggered || 0)} · 成功 ${Number(download.succeeded || 0)} · 失败 ${Number(download.failed || 0)} · 跳过 ${Number(download.skipped || 0)}`;
            } else {
                downloadText = `原图下载：已开启 · 等待任务 · 触发 ${Number(download.triggered || 0)} · 成功 ${Number(download.succeeded || 0)} · 失败 ${Number(download.failed || 0)}`;
            }
            if (download.last_error) {
                downloadText += ` · 最近错误：${ocrShortError(download.last_error, 100)}`;
            }
        }
        setOCRText('ocr-process-download', downloadText, download.last_error || '');
        panel.classList.toggle('is-active', Boolean(current.record_id || download.active));
        panel.classList.toggle('is-error', Boolean(download.stage === 'error' || download.last_error));
    }

    function renderOCRTechnicalDetails(data) {
        const container = byID('ocr-technical-details');
        if (!container) return;
        const index = data.index || {};
        const scope = data.scope || {};
        const localService = data.local_service || {};
        const download = data.auto_download || {};
        const current = data.current || {};
        const localServiceState = {
            not_required: '当前模式不需要',
            stopped: '等待自动启动',
            installing: '正在安装运行环境',
            installed: '运行环境已安装',
            starting: '正在自动启动',
            starting_backend: '正在启动 MLX 推理服务',
            warming_backend: '正在加载 GLM-OCR MLX 模型',
            starting_sdk: '正在启动 GLM-OCR SDK',
            warming: '正在加载模型',
            ready: '运行中',
            error: '启动失败',
        }[localService.state] || localService.state || '-';
        const entries = [
            ['服务地址', data.endpoint || '-'],
            ['模型与模式', `${ocrProviderLabel(data.provider, data.mode)} · ${data.model || '-'}`],
            ['待重试目标', `${ocrProviderLabel(data.provider, data.mode)} · ${data.endpoint || '-'}`],
            ['本地服务', localService.managed ? `${localServiceState}${localService.message ? ` · ${localService.message}` : ''}` : '外部服务或 API 模式'],
            ['模型进程同步', localService.managed
                ? (localService.synchronized ? 'MLX 与 GLM-OCR SDK 状态一致' : '状态不同步，启动或停止将同时修复两端')
                : '外部服务'],
            ['本地服务日志', localService.log_dir || '-'],
            ['实时监听', data.realtime_running ? '扫描器运行中' : '扫描器停止'],
            ['队列工作器', data.worker_running ? '运行中' : '停止'],
            ['当前任务阶段', current.record_id ? `${ocrProcessStageLabel(current.stage)} · ${current.detail || '-'}` : '无'],
            ['自动接收图片', download.enabled
                ? (!scope.all
                    ? '指定范围模式 · 名单内精准升级'
                    : (download.available ? '已开启' : '已开启（Frida 不可用）'))
                : '已关闭'],
            ['自动接收状态', download.stage ? `${ocrProcessStageLabel(download.stage)} · ${download.detail || '-'}` : '尚无记录'],
            ['兼容触发计数', `触发 ${Number(download.triggered || 0)} · 成功 ${Number(download.succeeded || 0)} · 失败 ${Number(download.failed || 0)} · 跳过 ${Number(download.skipped || 0)}`],
            ['最近处理错误', download.last_error || '无'],
            ['常驻 Frida', (() => {
                const d = download.frida_daemon || {};
                if (!d.running) return scope.all ? '未运行（OCR 启动时自动挂载）' : '未运行（名单 Hook 启动时自动挂载）';
                const cap = d.capture || {};
                const receive = cap.upgrade_receive
                    ? '全部接收升级'
                    : (cap.scoped_receive ? '名单精准升级' : '仅监听');
                const scoped = cap.scoped_receive
                    ? ` · 预置会话 ${Number(cap.scope_talker_count || 0)} · 会话命中 ${Number(cap.scope_talker_match_count || 0)}`
                    : '';
                return `运行中 · pid ${d.pid || '-'} · ${receive} · hits ${Number(cap.hits || 0)} · 自动升级 ${Number(cap.upgrade_count || 0)}${scoped} · 名单跳过 ${Number(cap.scoped_skip_count || 0)}`;
            })()],
            ['监听范围', scope.all ? '全部会话' : `联系人 ${(scope.contacts || []).length} · 群组 ${(scope.chatrooms || []).length}`],
            ['索引实现', index.fts_enabled ? 'FTS5 trigram 中文子串索引' : '字面量回退索引'],
            ['索引路径', index.index_path || '-'],
            ['启动时间', formatOCRTime(data.started_at)],
            ['最近成功', formatOCRTime(data.last_success_at)],
            ['扫描累计', `${Number(data.scanned || 0).toLocaleString('zh-CN')} 条消息`],
            ['入队累计', `${Number(data.enqueued || 0).toLocaleString('zh-CN')} 张图片`],
            ['处理累计', `${Number(data.processed || 0).toLocaleString('zh-CN')} 成功 · ${Number(data.failed || 0).toLocaleString('zh-CN')} 失败`],
            ['最后错误', data.last_error || '无'],
        ];
        container.replaceChildren();
        entries.forEach(([label, raw]) => {
            const row = document.createElement('div');
            const dt = document.createElement('span');
            dt.textContent = label;
            const dd = document.createElement('strong');
            dd.textContent = raw;
            dd.title = raw;
            row.append(dt, dd);
            container.appendChild(row);
        });
    }

    function renderOCRBackfill(backfill) {
        const panel = byID('ocr-backfill-progress');
        if (!panel) return;
        const state = backfill || {};
        const text = state.running
            ? `正在扫描 · 已检查 ${Number(state.scanned || 0).toLocaleString('zh-CN')} 条 · 新增 ${Number(state.enqueued || 0).toLocaleString('zh-CN')} 张`
            : state.finished_at
                ? `最近任务完成于 ${formatOCRTime(state.finished_at)} · 检查 ${Number(state.scanned || 0).toLocaleString('zh-CN')} 条 · 新增 ${Number(state.enqueued || 0).toLocaleString('zh-CN')} 张${state.error ? ` · ${state.error}` : ''}`
                : state.enabled ? '历史补录已开启，等待启动任务' : '历史补录已关闭';
        panel.textContent = text;
        panel.classList.toggle('is-running', Boolean(state.running));
        panel.classList.toggle('is-error', Boolean(state.error && state.error !== '已停止'));
        const enabledInForm = byID('ocr-backfill-enabled')?.checked;
        if (byID('ocr-backfill-start')) byID('ocr-backfill-start').disabled = !enabledInForm || state.running;
        if (byID('ocr-backfill-stop')) byID('ocr-backfill-stop').disabled = !state.running;
    }

    function renderOCRRouteOverview(data) {
        const index = data.index || {};
        const scope = data.scope || {};
        const localService = data.local_service || {};
        const apiMode = data.mode === 'api';
        const localBackend = localService.managed ? 'Apple MLX' : 'MLX / NVIDIA vLLM';
        const serviceReady = apiMode || !localService.managed || (
            localService.state === 'ready'
            && Boolean(localService.backend_ready)
            && Boolean(localService.sdk_ready)
            && localService.synchronized !== false
        );
        setOCRText(
            'ocr-route-title',
            apiMode
                ? '智谱 GLM-OCR API → OCR 索引'
                : `${localBackend} → GLM-OCR SDK → OCR 索引`,
        );
        setOCRText(
            'ocr-route-detail',
            apiMode
                ? `MaaS · ${data.model || 'glm-ocr'} · ${data.endpoint || '-'}`
                : `${serviceReady ? '服务就绪' : localService.message || '服务准备中'} · ${data.endpoint || '-'}`,
            localService.error || '',
        );
        setOCRText(
            'ocr-route-service',
            apiMode ? '智谱 MaaS' : localService.managed ? 'MLX + SDK' : '本地 SDK',
        );
        setOCRText(
            'ocr-route-scope',
            scope.all
                ? '全部会话'
                : `${(scope.contacts || []).length} 联系人 · ${(scope.chatrooms || []).length} 群组`,
        );
        const waitingMedia = Number(index.waiting_media || 0);
        const unfinished = Number(index.pending || 0) + Number(index.processing || 0) + Number(index.failed || 0);
        setOCRText('ocr-route-queue', `${unfinished.toLocaleString('zh-CN')} OCR 任务 · ${waitingMedia.toLocaleString('zh-CN')} 等待中图`);
        const note = byID('ocr-retry-sync-note');
        if (!note) return;
        note.classList.remove('is-success', 'is-error');
        if (ocrRetrySyncFeedback && ocrRetrySyncFeedback.until > Date.now()) {
            note.textContent = ocrRetrySyncFeedback.text;
            note.classList.add(ocrRetrySyncFeedback.error ? 'is-error' : 'is-success');
            return;
        }
        ocrRetrySyncFeedback = null;
        note.textContent = unfinished
            ? `${unfinished} 个未完成任务将使用 ${ocrProviderLabel(data.provider, data.mode)}；切换调用方式后会立即同步并重新排队。`
            : `当前没有待同步任务；后续任务将使用 ${ocrProviderLabel(data.provider, data.mode)}。`;
    }

    function renderOCRModelRuntime(data) {
        const local = data?.local_service || {};
        const managed = Boolean(local.managed);
        const backendReady = Boolean(local.backend_ready);
        const sdkReady = Boolean(local.sdk_ready);
        const pairSynchronized = local.synchronized !== false && backendReady === sdkReady;
        const pairReady = pairSynchronized && backendReady && sdkReady;
        const starting = ['starting', 'starting_backend', 'warming_backend', 'starting_sdk', 'warming'].includes(local.state);
        const anyProcessRunning = backendReady || sdkReady;
        setOCRText('ocr-model-backend-state', managed ? (backendReady ? '运行中' : '未运行') : '外部服务');
        setOCRText('ocr-model-backend-detail', managed ? `PID ${local.backend_pid || '-'} · 127.0.0.1:8080` : '当前地址不由 Chatlog 托管');
        setOCRText('ocr-model-sdk-state', managed ? (sdkReady ? '运行中' : '未就绪') : '外部服务');
        setOCRText('ocr-model-sdk-detail', managed ? `PID ${local.sdk_pid || '-'} · ${local.health_url || '-'}` : data?.endpoint || '-');
        setOCRText(
            'ocr-model-managed-state',
            managed ? (!pairSynchronized ? '状态不同步' : ({
                ready: '服务就绪',
                starting: '正在启动',
                starting_backend: '启动 MLX',
                warming_backend: '加载模型',
                starting_sdk: '启动 SDK',
                warming: 'SDK 预热',
                stopped: local.manual_stopped ? '已手动停止' : '已停止',
                error: '运行异常',
            }[local.state] || local.state || '等待状态')) : '未托管',
        );
        setOCRText(
            'ocr-model-managed-detail',
            managed
                ? `${pairSynchronized ? '双进程同步' : '双进程待同步'} · ${local.auto_start ? '自动启动' : '手动启动'} · ${local.auto_restart ? '自动重启' : '不自动重启'}${local.message ? ` · ${local.message}` : ''}`
                : '仅 Apple Silicon 默认 8081 本地地址提供进程控制',
            local.error || '',
        );
        const startButton = byID('ocr-local-start');
        if (startButton) {
            startButton.disabled = !managed || pairReady || starting;
            startButton.textContent = starting ? '模型同步启动中…' : pairReady ? '模型服务已同步运行' : anyProcessRunning ? '同步修复模型服务' : '同步启动模型服务';
            startButton.title = !managed
                ? '当前 OCR 地址由外部服务提供'
                : pairReady
                    ? 'MLX 与 GLM-OCR SDK 均已运行'
                    : starting
                        ? '正在按依赖顺序启动，并保证两端最终同时就绪'
                        : anyProcessRunning
                            ? '当前仅一端运行，点击后同步修复 MLX 与 GLM-OCR SDK'
                            : '同步启动 MLX 与 GLM-OCR SDK 服务';
        }
        const stopButton = byID('ocr-local-stop');
        if (stopButton) {
            stopButton.disabled = !managed || (!anyProcessRunning && !starting);
            stopButton.textContent = !managed ? '外部模型服务' : anyProcessRunning || starting ? '同步停止模型服务' : '模型服务已同步停止';
            stopButton.title = anyProcessRunning || starting
                ? '同时停止 MLX 与 GLM-OCR SDK 服务'
                : 'MLX 与 GLM-OCR SDK 当前均未运行';
        }
    }

    function renderOCRSearchStatus(data) {
        const index = data?.index || {};
        setOCRText('ocr-search-searchable', Number(index.searchable || 0).toLocaleString('zh-CN'));
        setOCRText('ocr-search-index-mode', index.fts_enabled ? 'FTS5 trigram 中文子串索引' : '字面量回退索引');
        setOCRText('ocr-search-succeeded', Number(index.succeeded || 0).toLocaleString('zh-CN'));
        setOCRText('ocr-search-provider', `${ocrProviderLabel(data?.provider, data?.mode)} · ${data?.model || '-'}`);
        setOCRText('ocr-search-queue', `${Number(index.pending || 0) + Number(index.processing || 0)} / ${Number(index.failed || 0)} · ${Number(index.waiting_media || 0)} 等待中图`);
        setOCRText('ocr-search-updated', index.last_updated ? `最近更新 ${formatOCRTime(index.last_updated)}` : '尚无索引更新');
    }

    async function loadOCRStatus() {
        return renderOCRStatus(await requestJSON('/api/v1/ocr/status'));
    }

    function renderOCRStatus(data) {
        const index = data.index || {};
        const waitingMediaCount = Number(index.waiting_media || 0);
        const retryWaitingButton = byID('ocr-retry-waiting-all');
        if (retryWaitingButton) {
            retryWaitingButton.disabled = waitingMediaCount === 0;
            retryWaitingButton.textContent = waitingMediaCount > 0
                ? `重试全部等待中图（${waitingMediaCount}）`
                : '重试全部等待中图';
        }
        const queue = Number(index.pending || 0) + Number(index.processing || 0);
        const total = Math.max(0, Number(index.total || 0) - waitingMediaCount);
        const succeeded = Number(index.succeeded || 0);
        const successRate = total ? (succeeded / total * 100).toFixed(1) : '0.0';
        const localService = data.local_service || {};
        const localServicePending = data.mode === 'local' &&
            localService.managed &&
            localService.state !== 'ready';
        const localServiceLabel = {
            stopped: '等待本地服务',
            installing: '安装 OCR 环境中',
            installed: '启动 OCR 服务中',
            starting: '启动 OCR 服务中',
            starting_backend: '启动 MLX 中',
            warming_backend: '加载 GLM-OCR 中',
            starting_sdk: '启动 SDK 中',
            warming: '加载模型中',
            error: '本地服务异常',
        }[localService.state] || '准备本地服务';
        const runningLabel = localServicePending
            ? localServiceLabel
            : data.realtime_running
            ? '实时识别中'
            : data.worker_running ? '仅处理队列' : '已停止';
        const activeModeLabel = data.mode === 'api'
            ? '智谱 API'
            : localService.managed ? 'Apple MLX · 本地 SDK' : '本地 GLM-OCR SDK';
        setOCRText('ocr-status-running', runningLabel);
        setOCRText(
            'ocr-status-mode',
            localServicePending
                ? localService.message || `${activeModeLabel} / ${data.model || '-'}`
                : `${activeModeLabel} / ${data.model || '-'}`,
            localService.error || '',
        );
        const current = data.current || {};
        const autoDownload = data.auto_download || {};
        const currentLabel = current.record_id
            ? `#${current.record_id} · ${ocrProcessStageLabel(current.stage)}${current.detail ? ` · ${current.detail}` : ''}`
            : autoDownload.active
                ? `原图下载中 · ${ocrProcessStageLabel(autoDownload.stage)}`
                : '当前无任务';
        setOCRText('ocr-status-queue', queue.toLocaleString('zh-CN'));
        setOCRText('ocr-status-current', currentLabel, current.media_key || autoDownload.media_key || '');
        setOCRText('ocr-status-success', succeeded.toLocaleString('zh-CN'));
        setOCRText('ocr-status-rate', `成功占比 ${successRate}%`);
        setOCRText('ocr-status-failed', Number(index.failed || 0).toLocaleString('zh-CN'));
        setOCRText('ocr-status-error', data.last_error ? String(data.last_error).slice(0, 120) : '无错误', data.last_error || '');
        setOCRText('ocr-status-searchable', Number(index.searchable || 0).toLocaleString('zh-CN'));
        setOCRText('ocr-status-index', index.fts_enabled ? 'FTS5 trigram' : 'literal fallback', index.index_path || '');
        setOCRText('ocr-status-scan', formatOCRTime(data.last_scan_at));
        setOCRText('ocr-status-scan-detail', `${Number(data.last_scan_duration_ms || 0)} ms · ${Number(data.last_scan_messages || 0)} 条 · ${Number(data.last_scan_enqueued || 0)} 入队`);
        const pageState = byID('ocr-page-state');
        if (pageState) {
            const downloadHint = autoDownload.active
                ? ` · 原图${ocrProcessStageLabel(autoDownload.stage)}`
                : '';
            pageState.textContent = `${runningLabel} · 排队 ${queue} · 失败 ${Number(index.failed || 0)}${downloadHint}`;
            pageState.classList.toggle('is-error', Boolean(data.last_error || localService.error || autoDownload.stage === 'error'));
        }
        renderOCRProcessStatus(data);
        renderOCRBackfill(data.backfill);
        renderOCRTaskList(data.recent);
        renderOCRTechnicalDetails(data);
        renderOCRRouteOverview(data);
        renderOCRModelRuntime(data);
        renderOCRSearchStatus(data);
        return data;
    }

    window.ocrCenterRefresh = async () => {
        try {
            await Promise.all([loadOCRConfig(), loadOCRStatus(), loadOCRScopeOptions()]);
        } catch (error) {
            setOCRText('ocr-page-state', `读取失败：${error.message || error}`);
        }
    };

    window.ocrCenterSaveConfig = async (options = {}) => {
        const endpoint = value('ocr-endpoint');
        if (!absoluteHTTPURL(endpoint)) {
            setOCRText('ocr-page-state', '服务地址格式错误');
            byID('ocr-endpoint')?.focus();
            return false;
        }
        const poll = optionalInteger('ocr-poll-interval', 1, 3600);
        const timeout = optionalInteger('ocr-request-timeout', 10, 1800);
        if (!poll.valid || !timeout.valid) {
            setOCRText('ocr-page-state', '运行参数必须是有效整数');
            return false;
        }
        const scopeAll = value('ocr-scope-mode') !== 'selected';
        if (!scopeAll && !ocrSelectedContacts.size && !ocrSelectedChatRooms.size) {
            setOCRText('ocr-page-state', '请选择至少一个联系人或群组');
            return false;
        }
        const apiKey = value('ocr-api-key');
        const payload = {
            enabled: Boolean(byID('ocr-realtime-enabled')?.checked),
            backfill_enabled: Boolean(byID('ocr-backfill-enabled')?.checked),
            auto_download_original: Boolean(byID('ocr-auto-download-enabled')?.checked),
            local_auto_start: Boolean(byID('ocr-local-auto-start')?.checked),
            local_auto_restart: Boolean(byID('ocr-local-auto-restart')?.checked),
            mode: ocrModeValue(),
            endpoint,
            model: value('ocr-model') || 'glm-ocr',
            poll_interval_sec: Number(poll.value),
            request_timeout_sec: Number(timeout.value),
            received_only: Boolean(byID('ocr-received-only')?.checked),
            scope_all: scopeAll,
            listen_contacts: scopeAll ? [] : ocrCenterSelectedValues('contact'),
            listen_chatrooms: scopeAll ? [] : ocrCenterSelectedValues('chatroom'),
            clear_api_key: Boolean(byID('ocr-clear-api-key')?.checked),
        };
        if (apiKey) payload.api_key = apiKey;
        setOCRText('ocr-page-state', '正在保存、切换调用方式并同步任务…');
        try {
            const saved = await requestJSON('/api/v1/ocr/config', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            });
            const sync = saved.retry_sync || {};
            if (sync.changed) {
                const requeued = Number(sync.requeued || 0);
                ocrRetrySyncFeedback = {
                    text: sync.error
                        ? `调用方式已切换，但任务同步出现异常：${sync.error}`
                        : `调用方式已切换为 ${ocrProviderLabel(sync.provider, saved.mode)}；${requeued} 个未完成任务已更新并重新排队。`,
                    error: Boolean(sync.error),
                    until: Date.now() + 15000,
                };
            }
            await Promise.all([loadOCRConfig(), loadOCRStatus()]);
            if (!options.silent) {
                setOCRText(
                    'ocr-page-state',
                    sync.changed
                        ? sync.error
                            ? '配置已生效，任务同步需要检查'
                            : `配置已生效 · 已同步 ${Number(sync.requeued || 0)} 个未完成任务`
                        : '配置已保存并立即生效',
                );
            }
            return true;
        } catch (error) {
            setOCRText('ocr-page-state', `保存失败：${error.message || error}`);
            return false;
        }
    };

    window.ocrCenterSearch = async () => {
        const keyword = value('ocr-search-keyword');
        if (!keyword) {
            showError('ocr-search-result', '图片文字关键词不能为空');
            return;
        }
        const params = new URLSearchParams({ keyword });
        appendParam(params, 'chats', value('ocr-search-chats'));
        appendParam(params, 'time', value('ocr-search-time'));
        params.set('limit', String(numberValue('ocr-search-limit', 50, 1, 500)));
        params.set('offset', String(numberValue('ocr-search-offset', 0, 0, 5000)));
        setBusy('ocr-search-result', '正在使用 OCR 中文索引搜索图片…');
        try {
            const data = await requestJSON(`/api/v1/ocr/search?${params}`);
            renderStructured('ocr-search-result', data, '图片文字搜索');
            setOCRText('ocr-search-page-state', `搜索完成 · ${data.count ?? data.messages?.length ?? 0} 条`);
        } catch (error) {
            showError('ocr-search-result', error);
            setOCRText('ocr-search-page-state', '搜索失败');
        }
    };

    window.ocrSearchRefreshStatus = async () => {
        try {
            const data = await loadOCRStatus();
            renderOCRSearchStatus(data);
            setOCRText('ocr-search-page-state', '索引状态已刷新');
        } catch (error) {
            setOCRText('ocr-search-page-state', `状态读取失败：${error.message || error}`);
        }
    };

    window.ocrCenterLocalServiceAction = async (action) => {
        if (!['start', 'stop'].includes(action)) return;
        if (action === 'stop' && !window.confirm('同步停止 MLX 与 GLM-OCR SDK 服务？未完成任务会保留在持久队列中。')) return;
        setOCRText('ocr-page-state', action === 'start' ? '正在同步启动模型服务…' : '正在同步停止模型服务…');
        try {
            const result = await requestJSON(`/api/v1/ocr/local-service/${action}`, { method: 'POST' });
            if (action === 'stop') {
                const autoRestart = byID('ocr-local-auto-restart');
                if (autoRestart) autoRestart.checked = false;
            }
            setOCRText('ocr-page-state', action === 'start' ? '双进程同步启动指令已提交' : '双进程已同步停止');
            await Promise.all([
                loadOCRStatus(),
                action === 'stop' ? loadOCRConfig() : Promise.resolve(result),
            ]);
        } catch (error) {
            setOCRText('ocr-page-state', `${action === 'start' ? '启动' : '停止'}失败：${error.message || error}`);
        }
    };

    window.ocrCenterStartBackfill = async () => {
        if (ocrBackfillRequestActive) return;
        if (!byID('ocr-backfill-enabled')?.checked) {
            setOCRText('ocr-page-state', '请先开启历史补录');
            return;
        }
        const limit = optionalInteger('ocr-backfill-limit', 1, 10000);
        if (!limit.valid) {
            setOCRText('ocr-page-state', '补录数量范围为 1–10000');
            return;
        }
        if (!window.confirm(`开始扫描历史图片，本次最多加入 ${limit.value} 张？`)) return;
        if (!await window.ocrCenterSaveConfig({ silent: true })) return;
        ocrBackfillRequestActive = true;
        setOCRText('ocr-page-state', '历史补录扫描中');
        try {
            await requestJSON('/api/v1/ocr/backfill', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    limit: Number(limit.value),
                    received_only: Boolean(byID('ocr-backfill-received-only')?.checked),
                    respect_scope: Boolean(byID('ocr-backfill-respect-scope')?.checked),
                }),
            });
            setOCRText('ocr-page-state', '历史补录扫描完成，队列继续处理');
        } catch (error) {
            setOCRText('ocr-page-state', `历史补录失败：${error.message || error}`);
        } finally {
            ocrBackfillRequestActive = false;
            await loadOCRStatus().catch(() => null);
        }
    };

    window.ocrCenterStopBackfill = async () => {
        try {
            await requestJSON('/api/v1/ocr/backfill/stop', { method: 'POST' });
            setOCRText('ocr-page-state', '历史补录已停止');
            await loadOCRStatus();
        } catch (error) {
            setOCRText('ocr-page-state', `停止失败：${error.message || error}`);
        }
    };

    window.ocrCenterRetry = async (id) => {
        const recordID = Number(id);
        if (!Number.isSafeInteger(recordID) || recordID <= 0) return;
        try {
            await requestJSON(`/api/v1/ocr/index/${encodeURIComponent(String(recordID))}/retry`, { method: 'POST' });
            setOCRText('ocr-page-state', `记录 #${recordID} 已重新排队`);
            await loadOCRStatus();
        } catch (error) {
            setOCRText('ocr-page-state', `重试失败：${error.message || error}`);
        }
    };

    window.ocrCenterRetryWaitingMedia = async (id) => {
        const recordID = Number(id);
        if (!Number.isSafeInteger(recordID) || recordID <= 0) return;
        setOCRText('ocr-page-state', `正在重试记录 #${recordID} 的中图下载…`);
        try {
            const data = await requestJSON(`/api/v1/ocr/index/${encodeURIComponent(String(recordID))}/media-retry`, { method: 'POST' });
            const result = data.result || {};
            const message = Number(result.promoted || 0) > 0
                ? `记录 #${recordID} 已检测到中图并进入 OCR 队列`
                : Number(result.triggered || 0) > 0
                    ? `记录 #${recordID} 已向微信提交原生下载请求，正在等待落盘`
                    : Number(result.armed || 0) > 0
                    ? `记录 #${recordID} 已刷新下载预授权，等待下一轮提交`
                    : `记录 #${recordID} 已完成本地检查`;
            setOCRText('ocr-page-state', message);
            await loadOCRStatus();
        } catch (error) {
            setOCRText('ocr-page-state', `中图重试失败：${error.message || error}`);
        }
    };

    window.ocrCenterRetryWaitingMediaAll = async () => {
        const button = byID('ocr-retry-waiting-all');
        if (button) button.disabled = true;
        setOCRText('ocr-page-state', '正在检查并重试全部等待中图…');
        try {
            const data = await requestJSON('/api/v1/ocr/index/waiting-media/retry', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ limit: 500 }),
            });
            const result = data.result || {};
            setOCRText(
                'ocr-page-state',
                `等待中图重试完成 · 原生请求已提交 ${Number(result.triggered || 0)} · 预授权 ${Number(result.armed || 0)} · 已入队 ${Number(result.promoted || 0)} · 跳过 ${Number(result.skipped || 0)} · 失败 ${Number(result.failed || 0)}`,
            );
            await loadOCRStatus();
        } catch (error) {
            setOCRText('ocr-page-state', `批量中图重试失败：${error.message || error}`);
            if (button) button.disabled = false;
        }
    };

    window.ocrCenterRerecognize = async (id) => {
        const recordID = Number(id);
        if (!Number.isSafeInteger(recordID) || recordID <= 0) return;
        if (!window.confirm(`重新识别记录 #${recordID}？当前文字、描述和版面结果会先从索引中移除。`)) return;
        setOCRText('ocr-page-state', `正在重新排队记录 #${recordID}…`);
        try {
            const result = await requestJSON(`/api/v1/ocr/index/${encodeURIComponent(String(recordID))}/rerecognize`, { method: 'POST' });
            setOCRText(
                'ocr-page-state',
                result.requeued
                    ? `记录 #${recordID} 已按当前模式重新排队`
                    : `记录 #${recordID} 当前状态不适合重新识别`,
            );
            await loadOCRStatus();
        } catch (error) {
            setOCRText('ocr-page-state', `重新识别失败：${error.message || error}`);
        }
    };

    function setOCRDetailContent(id, content) {
        const target = byID(id);
        if (target) target.textContent = String(content || '').trim() || '-';
    }

    window.ocrCenterOpenDetail = async (id) => {
        const recordID = Number(id);
        if (!Number.isSafeInteger(recordID) || recordID <= 0) return;
        const modal = byID('ocr-detail-modal');
        if (!modal) return;
        modal.classList.remove('hidden');
        document.body.classList.add('modal-open');
        setOCRDetailContent('ocr-detail-title', `OCR 记录 #${recordID}`);
        setOCRDetailContent('ocr-detail-meta', '正在读取完整识别内容…');
        ['ocr-detail-description', 'ocr-detail-text', 'ocr-detail-markdown', 'ocr-detail-layout'].forEach((target) => setOCRDetailContent(target, '正在读取…'));
        const image = byID('ocr-detail-image');
        const imageEmpty = byID('ocr-detail-image-empty');
        if (image) {
            image.removeAttribute('src');
            image.classList.add('hidden');
        }
        if (imageEmpty) imageEmpty.classList.remove('hidden');
        try {
            const record = await requestJSON(`/api/v1/ocr/index/${encodeURIComponent(String(recordID))}`);
            setOCRDetailContent('ocr-detail-title', record.talker_name || record.talker || `OCR 记录 #${recordID}`);
            setOCRDetailContent(
                'ocr-detail-meta',
                `#${recordID} · ${formatOCRTime(record.message_time)} · ${ocrProviderLabel(record.provider)} / ${record.model || '-'} · ${record.status || '-'}`,
            );
            setOCRDetailContent('ocr-detail-description', record.description);
            setOCRDetailContent('ocr-detail-text', record.ocr_text);
            setOCRDetailContent('ocr-detail-markdown', record.markdown);
            setOCRDetailContent('ocr-detail-layout', record.layout ? JSON.stringify(record.layout, null, 2) : '');
            const facts = byID('ocr-detail-facts');
            if (facts) {
                facts.replaceChildren();
                [
                    ['会话', record.talker_name || record.talker],
                    ['发送者', record.sender_name || record.sender || (record.is_self ? '我' : '-')],
                    ['尝试次数', record.attempts || 0],
                    ['消息位置', `${record.message_seq || '-'} / ${record.db_local_id || '-'}`],
                    ['内容哈希', record.content_hash || '-'],
                    ['媒体路径', record.media_path || record.media_key || '-'],
                ].forEach(([label, value]) => {
                    const term = document.createElement('dt');
                    term.textContent = label;
                    const detail = document.createElement('dd');
                    detail.textContent = String(value ?? '-');
                    facts.append(term, detail);
                });
            }
            if (image && record.image_url) {
                image.onload = () => {
                    image.classList.remove('hidden');
                    if (imageEmpty) imageEmpty.classList.add('hidden');
                };
                image.onerror = () => {
                    image.classList.add('hidden');
                    if (imageEmpty) {
                        imageEmpty.textContent = '原图预览读取失败';
                        imageEmpty.classList.remove('hidden');
                    }
                };
                image.src = record.image_url;
            }
        } catch (error) {
            setOCRDetailContent('ocr-detail-meta', `读取失败：${error.message || error}`);
            setOCRDetailContent('ocr-detail-description', '读取 OCR 详情失败');
            setOCRDetailContent('ocr-detail-text', '');
            setOCRDetailContent('ocr-detail-markdown', '');
            setOCRDetailContent('ocr-detail-layout', '');
        }
    };

    window.ocrCenterCloseDetail = () => {
        byID('ocr-detail-modal')?.classList.add('hidden');
        document.body.classList.remove('modal-open');
    };

    window.ocrCenterClearSucceeded = async () => {
        if (!window.confirm('清空全部已识别 OCR 内容和搜索索引？图片原文件、等待任务和失败任务会保留。')) return;
        setOCRText('ocr-page-state', '正在清空已识别 OCR 内容…');
        try {
            const result = await requestJSON('/api/v1/ocr/index/succeeded', { method: 'DELETE' });
            window.ocrCenterCloseDetail();
            setOCRText('ocr-page-state', `已清空 ${Number(result.removed || 0)} 条识别内容`);
            await loadOCRStatus();
        } catch (error) {
            setOCRText('ocr-page-state', `清空失败：${error.message || error}`);
        }
    };

    async function initializeOCRCenter() {
        if (!ocrConfigLoaded) {
            await Promise.all([loadOCRConfig(), loadOCRScopeOptions()]);
        }
        await loadOCRStatus();
    }

    async function initializeOCRSearch() {
        const data = await loadOCRStatus();
        renderOCRSearchStatus(data);
    }

    window.contactLoadOpenIMCorp = async () => {
        const wxid = value('contact-openim-wxid');
        if (!wxid) {
            showError('contact-openim-result', 'OpenIM wxid 不能为空');
            return;
        }
        if (!wxid.toLowerCase().endsWith('@openim')) {
            showError('contact-openim-result', 'OpenIM wxid 必须以 @openim 结尾');
            return;
        }
        setBusy('contact-openim-result', '正在读取企业信息…');
        try {
            const data = await requestJSON(`/api/v1/openim/${encodeURIComponent(wxid)}/corp`);
            renderStructured('contact-openim-result', [data], '企业微信联系人');
        } catch (error) {
            showError('contact-openim-result', error);
        }
    };

    async function socialRequest(containerID, endpoint, params, label) {
        setBusy(containerID, `正在读取${label}…`);
        try {
            const data = await requestJSON(`${endpoint}?${params}`);
            renderStructured(containerID, data, label);
        } catch (error) {
            showError(containerID, error);
        }
    }

    window.socialLoadFeed = () => {
        const params = new URLSearchParams();
        appendParam(params, 'user', value('sns-feed-user'));
        appendParam(params, 'time', value('sns-feed-time'));
        params.set('limit', String(numberValue('sns-feed-limit', 50, 1)));
        return socialRequest('sns-feed-result', '/api/v1/sns_feed', params, '朋友圈动态');
    };

    window.socialSearch = () => {
        const keyword = value('sns-search-keyword');
        if (!keyword) {
            showError('sns-search-result', '关键词不能为空');
            return;
        }
        const params = new URLSearchParams({ keyword });
        appendParam(params, 'user', value('sns-search-user'));
        appendParam(params, 'time', value('sns-search-time'));
        params.set('limit', String(numberValue('sns-search-limit', 50, 1)));
        return socialRequest('sns-search-result', '/api/v1/sns_search', params, '朋友圈搜索');
    };

    window.socialLoadNotifications = () => {
        const params = new URLSearchParams();
        appendParam(params, 'time', value('sns-notification-time'));
        params.set('include_read', byID('sns-notification-read')?.checked ? '1' : '0');
        params.set('limit', String(numberValue('sns-notification-limit', 50, 1)));
        return socialRequest('sns-notification-result', '/api/v1/sns_notifications', params, '朋友圈通知');
    };

    window.socialLoadFavorites = () => {
        const favoriteType = optionalInteger('favorite-type', 0);
        if (!favoriteType.valid) {
            showError('favorite-result', '收藏类型必须是非负整数');
            return;
        }
        const params = new URLSearchParams();
        appendParam(params, 'query', value('favorite-query'));
        appendParam(params, 'fav_type', favoriteType.value);
        params.set('limit', String(numberValue('favorite-limit', 50, 1)));
        return socialRequest('favorite-result', '/api/v1/favorites', params, '微信收藏');
    };

    function renderMediaTool(url, type, label) {
        const container = byID('media-tool-preview');
        if (!container) return;
        container.replaceChildren();
        const reference = { url: new URL(url, location.origin).href, type, label };
        container.appendChild(mediaElement(reference));
        const link = document.createElement('a');
        link.href = reference.url;
        link.target = '_blank';
        link.rel = 'noopener';
        link.className = 'btn btn-secondary btn-sm';
        link.textContent = '在新窗口打开';
        container.appendChild(link);
    }

    window.mediaToolOpenMessage = async () => {
        const type = value('media-tool-type') || 'image';
        const key = value('media-tool-key');
        if (!key) {
            showError('media-tool-preview', 'Media Key 不能为空');
            return;
        }
        const base = `/${type}/${encodeURIComponent(key)}`;
        if (byID('media-tool-info')?.checked) {
            setBusy('media-tool-preview', '正在读取媒体元数据…');
            try {
                const data = await requestJSON(`${base}?info=1`);
                renderStructured('media-tool-preview', [data], '媒体元数据');
            } catch (error) {
                showError('media-tool-preview', error);
            }
            return;
        }
        renderMediaTool(base, type, type === 'file' ? '附件' : type);
    };

    window.mediaToolOpenData = () => {
        const rawPath = value('media-tool-data-path').replace(/^\/+/, '');
        if (!rawPath) {
            showError('media-tool-preview', '数据文件相对路径不能为空');
            return;
        }
        if (!validRelativeDataPath(value('media-tool-data-path'))) {
            showError('media-tool-preview', '数据文件必须是账号目录内的相对路径，不能包含 ..');
            return;
        }
        const encoded = rawPath.split('/').map(encodeURIComponent).join('/');
        renderMediaTool(`/data/${encoded}`, 'file', '数据文件');
    };

    window.mediaToolOpenSNS = () => {
        const rawURL = value('media-tool-sns-url');
        if (!rawURL) {
            showError('media-tool-preview', '朋友圈媒体 URL 不能为空');
            return;
        }
        if (!absoluteHTTPURL(rawURL)) {
            showError('media-tool-preview', '朋友圈媒体 URL 必须是无账号信息的完整 http:// 或 https:// 地址');
            return;
        }
        const key = value('media-tool-sns-key');
        if (!validUint64(key)) {
            showError('media-tool-preview', '解密 Key 必须是 uint64 范围内的十进制整数');
            return;
        }
        const params = new URLSearchParams({ url: rawURL });
        appendParam(params, 'key', key);
        const type = value('media-tool-sns-type') || 'image';
        renderMediaTool(`/api/v1/sns/media/proxy?${params}`, type, '朋友圈媒体');
    };

    function apiStatusCard(label, value, hint, state = '') {
        const card = document.createElement('article');
        card.className = `hook-status-card ${state}`;
        const span = document.createElement('span');
        span.textContent = label;
        const strong = document.createElement('strong');
        strong.textContent = value;
        const small = document.createElement('small');
        small.textContent = hint;
        card.append(span, strong, small);
        return card;
    }

    window.apiInspectorLoad = async () => {
        const summary = byID('api-inspector-summary');
        if (summary) summary.innerHTML = '<div class="hook-status-card is-loading"><span>契约状态</span><strong>正在读取…</strong></div>';
        try {
            const [ping, meta, openapi] = await Promise.all([
                requestJSON('/api/v1/ping'),
                requestJSON('/api/v1/meta'),
                requestJSON('/api/v1/openapi.json'),
            ]);
            catalogData = meta;
            openAPIData = openapi;
            if (summary) {
                summary.replaceChildren(
                    apiStatusCard('服务 Ping', ping.ok === false ? '异常' : '正常', ping.version || 'JSON API 可访问', ping.ok === false ? 'is-error' : 'is-ok'),
                    apiStatusCard('Catalog', `${meta.total || meta.endpoints?.length || 0} operations`, `Contract ${meta.contract_version || meta.version || '-'}`, 'is-accent'),
                    apiStatusCard('OpenAPI', openapi.openapi || '-', `${Object.keys(openapi.paths || {}).length} paths`, 'is-ok'),
                    apiStatusCard('前端接入', `${meta.total || meta.endpoints?.length || 0} operations`, '由 Catalog 覆盖测试持续校验', 'is-accent'),
                );
            }
            window.apiInspectorRenderCatalog();
        } catch (error) {
            if (summary) {
                summary.replaceChildren(apiStatusCard('契约读取失败', '不可用', error.message, 'is-error'));
            }
            showError('api-inspector-catalog', error);
        }
    };

    function catalogEndpoints() {
        if (Array.isArray(catalogData?.endpoints)) return catalogData.endpoints;
        return [];
    }

    window.apiInspectorRenderCatalog = () => {
        const container = byID('api-inspector-catalog');
        if (!container) return;
        const filter = value('api-inspector-filter').toLowerCase();
        const endpoints = catalogEndpoints().filter((endpoint) => {
            const haystack = `${endpoint.name} ${endpoint.method} ${endpoint.path} ${endpoint.category} ${endpoint.summary}`.toLowerCase();
            return !filter || haystack.includes(filter);
        });
        container.replaceChildren();
        if (!endpoints.length) {
            container.innerHTML = '<div class="analytics-empty">没有匹配的接口</div>';
            return;
        }
        const table = document.createElement('table');
        table.innerHTML = '<thead><tr><th>方法</th><th>名称</th><th>路径</th><th>分类</th><th>响应</th></tr></thead>';
        const body = document.createElement('tbody');
        endpoints.forEach((endpoint) => {
            const row = document.createElement('tr');
            [endpoint.method, endpoint.name, endpoint.path, endpoint.category, endpoint.response?.mode || 'json'].forEach((text, index) => {
                const cell = document.createElement('td');
                cell.textContent = String(text || '-');
                if (index === 0) cell.className = `method method-${String(text || '').toLowerCase()}`;
                row.appendChild(cell);
            });
            body.appendChild(row);
        });
        table.appendChild(body);
        container.appendChild(table);
    };

    window.apiInspectorCopyCatalog = async () => {
        if (!catalogData) await window.apiInspectorLoad(true);
        await navigator.clipboard.writeText(JSON.stringify(catalogData, null, 2));
    };

    window.apiInspectorDownloadOpenAPI = async () => {
        if (!openAPIData) await window.apiInspectorLoad(true);
        const blob = new Blob([JSON.stringify(openAPIData, null, 2)], { type: 'application/json' });
        const url = URL.createObjectURL(blob);
        const anchor = document.createElement('a');
        anchor.href = url;
        anchor.download = 'chatlog-openapi.json';
        anchor.click();
        window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    };

    window.chatlogWorkspaceInitialize = async (tabID) => {
        if (tabID === 'api-inspector') await window.apiInspectorLoad();
        if (tabID === 'ocr-center') await initializeOCRCenter();
        if (tabID === 'ocr-search') await initializeOCRSearch();
    };

    window.addEventListener('chatlog:ocr-status', event => {
        if (event.detail) renderOCRStatus(event.detail);
    });
    window.addEventListener('chatlog:new-message', () => {
        if (!document.hidden && byID('message-center')?.classList.contains('active') && byID('mc-new-auto')?.checked) {
            window.messageCenterLoadNew();
        }
    });
    document.addEventListener('visibilitychange', () => {
        if (document.hidden) return;
        if (byID('mc-new-auto')?.checked) window.messageCenterTogglePolling();
    });
})();
