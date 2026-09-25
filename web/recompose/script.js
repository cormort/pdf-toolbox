// ==========================================================
// ===   主程式進入點 (window.onload)
// ==========================================================
window.onload = function() {

    // ------------------------------------------------------
    // 0. 全域錯誤防護
    // 說明：HTML 的 onclick 呼叫到 async 函式（removeFile / generatePDF ...）時，
    // 若 Promise 被拒絕，瀏覽器只會印 console error，使用者端完全沒反應。
    // 這裡統一攔下來提示，避免「按了沒動靜」的無聲失敗。
    // ------------------------------------------------------
    window.addEventListener('unhandledrejection', (e) => {
        const msg = (e.reason && e.reason.message) ? e.reason.message : String(e.reason);
        console.error('未處理的 Promise 錯誤：', e.reason);
        if (typeof showNotification === 'function') showNotification('❌ 操作失敗：' + msg, 'error');
    });

    // ------------------------------------------------------
    // 1. 函式庫設定與全域變數 (Configuration & State)
    // ------------------------------------------------------
    
    // 設定 PDF.js worker 路徑
    pdfjsLib.GlobalWorkerOptions.workerSrc = './pdf.worker.min.js';

    // 資料儲存容器
    let pdfFiles = [];          // 存放載入的 PDF 檔案資訊
    let selectedPages = [];     // 存放右側已選擇的頁面資訊
    
    // 介面狀態變數
    let viewMode = 'list';      // 左側檢視模式 (list/grid)
    let thumbnailSize = 'medium'; // 左側縮圖大小

    // 預設檢視模式
    let targetViewMode = 'list'; // 右側檢視模式
    let targetThumbnailSize = 'medium'; // 右側縮圖大小

    // 操作輔助變數
    let lastSourceClickGlobalIndex = null; // (Shift多選) 上次點擊的全域索引
    let clearFilesConfirmMode = false;    // 清除檔案確認鎖
    let clearSelectedConfirmMode = false; // 清除已選確認鎖
    let isLoadingFiles = false;           // 載入中鎖：避免連續拖入造成 pdfFiles 疊加與進度錯亂
    let pendingFileQueue = [];            // 載入中抵達的檔案先排隊，載完再處理（不丟棄）

    // 縮圖延遲渲染：頁面很多時只先建立骨架，捲到可見範圍才放 <img>
    let sourceObserver = null;            // IntersectionObserver
    let sourceRenderToken = 0;            // 每次重繪 +1，用來讓舊的 observer 失效

    // 來源 PDF 的書籤大綱：檔名 -> { pageIndex: {title, level} }
    // 載入時就抽出來（pdf.js 文件之後會 destroy），用來預填目錄與寫入成品書籤。
    let sourceOutlines = new Map();

    // --- 復原／重做 ---
    // 快照只複製「會被改到的結構」（頁面屬性、selectedPages），縮圖 data URL 只共用參考，
    // 所以每個快照的額外記憶體很小；真正的資料量只在寫入 IndexedDB 時才產生。
    const HISTORY_LIMIT = 60;
    const HISTORY_MERGE_MS = 400;   // 同類操作在此毫秒內合併成一步（例如連續拖曳排序）
    let undoStack = [];
    let redoStack = [];
    let lastHistoryLabel = null;
    let lastHistoryAt = 0;
    let historyDepth = 0;           // 防止 undo/redo 本身再觸發記錄
    let isRestoringSession = false;

    // --- 工作階段保存 ---
    const SESSION_DB = 'pdf-recompose';
    const SESSION_STORE = 'session';
    const SESSION_KEY = 'current';
    const SESSION_SAVE_DEBOUNCE_MS = 1200;
    const SESSION_MAX_DOC_BYTES = 80 * 1024 * 1024; // 超過就不自動存（避免撞配額）
    let sessionSaveTimer = null;
    let sessionSaveInFlight = false;
    let sessionSavePending = false;
    let sessionEnabled = true;
    let lastExportZip = null;   // 最近一次拆檔產生的 ZIP（供測試取用）
    let sessionDirty = false;   // 有沒有「還沒寫進去的變更」
    let sessionSavePromise = null; // 進行中的寫入（清空工作階段時要等它結束）

    // PDF 預覽相關
    let finalPdfBytes = null;
    let currentPreviewUrl = null;

    // 可調參數
    const MAX_FILE_BYTES = 300 * 1024 * 1024; // 單檔上限（超過只警告，不阻擋）
    const TOC_LINE_HEIGHT_DEFAULT = 20;

    // ------------------------------------------------------
    // 2. 函式庫檢查 (Dependency Check)
    // ------------------------------------------------------
    if (typeof PDFLib === 'undefined') {
        console.error("CRITICAL: PDFLib is not defined!");
        showNotification("錯誤：PDF 編輯函式庫 (pdf-lib.min.js) 載入失敗。", 'error');
        return;
    }
    if (typeof fontkit === 'undefined') {
        console.error("CRITICAL: fontkit is not defined!");
        showNotification("錯誤：字型工具函式庫 (fontkit.umd.min.js) 載入失敗。", 'error');
        return;
    }
    if (typeof Sortable === 'undefined') {
        console.error("CRITICAL: Sortable is not defined!");
        showNotification("錯誤：拖曳函式庫 (sortable.min.js) 載入失敗。", 'error');
        return;
    }

    // ------------------------------------------------------
    // 3. DOM 元素快取 (DOM Elements)
    // ------------------------------------------------------
    const uploadArea = document.getElementById('uploadArea');
    const fileInput = document.getElementById('fileInput');
    const fileList = document.getElementById('fileList');
    const sourcePanel = document.getElementById('sourcePanel');
    const sourcePages = document.getElementById('sourcePages');
    const selectedPagesContainer = document.getElementById('selectedPages');
    const progress = document.getElementById('progress');
    const tocModal = document.getElementById('tocModal');
    const tocTextarea = document.getElementById('tocTextarea');
    const notification = document.getElementById('notification');
    const addTocCheckbox = document.getElementById('addTocCheckbox');
    const tocSettingsPanel = document.getElementById('tocSettingsPanel');
    const marksSettingsPanel = document.getElementById('marksSettingsPanel');
    const watermarkSettingsPanel = document.getElementById('watermarkSettingsPanel');
    const layoutSettingsPanel = document.getElementById('layoutSettingsPanel');
    const impositionSettingsPanel = document.getElementById('impositionSettingsPanel');
    const splitSettingsPanel = document.getElementById('splitSettingsPanel');
    const previewModal = document.getElementById('previewModal');

    // ------------------------------------------------------
    // 4. 公開函式註冊 (Export to Window)
    // 說明：為了讓 HTML 中的 onclick="" 能夠呼叫，必須掛載到 window 下
    // ------------------------------------------------------
    
    // 基礎與檔案操作
    window.updateFileList = updateFileList;
    window.removeFile = removeFile;
    window.clearAllFiles = clearAllFiles;
    
    // 左側 (來源) 面板操作
    window.setViewMode = setViewMode;
    window.setThumbnailSize = setThumbnailSize;
    window.toggleSourceCheck = toggleSourceCheck;
    window.toggleSelectAllSource = toggleSelectAllSource;
    window.batchAddToTarget = batchAddToTarget;
    window.batchDeleteFromSource = batchDeleteFromSource;
    window.batchRotateSource = batchRotateSource;
    window.updateQuickSelectFileOptions = updateQuickSelectFileOptions;
    window.applyQuickSelection = applyQuickSelection;
    window.clearAllSourceChecks = clearAllSourceChecks;

    // 右側 (成品) 面板操作
    window.setTargetViewMode = setTargetViewMode;
    window.setTargetThumbnailSize = setTargetThumbnailSize;
    window.toggleTargetCheck = toggleTargetCheck;
    window.toggleSelectAllTarget = toggleSelectAllTarget;
    window.applyTargetQuickSelection = applyTargetQuickSelection;
    window.batchRotateTarget = batchRotateTarget;
    window.batchDeleteFromTarget = batchDeleteFromTarget;
    window.removeSelectedPage = removeSelectedPage;
    window.clearSelectedPages = clearSelectedPages;
    window.addSectionDivider = addSectionDivider;
    window.insertBlankPage = insertBlankPage;

    // 目錄與設定
    window.openTocEditor = openTocEditor;
    window.closeTocEditor = closeTocEditor;
    window.saveToc = saveToc;
    window.prefillTocFromSource = prefillTocFromSource;
    window.resetTocSettings = resetTocSettings;
    window.resetWatermarkSettings = resetWatermarkSettings;
    window.resetLayoutSettings = resetLayoutSettings;
    window.resetImpositionSettings = resetImpositionSettings;
    window.resetSplitSettings = resetSplitSettings;
    // 驗證用：把最後一個小節移到最前面（等同拖曳），讓「開頭小節」的情境可測
    // 驗證用：目前右側的分組（每個小節含哪些頁）
    // 驗證用：編號方案的純函式（轉換規則用單元檢查最直接）
    window.__numbering = { formatNumberByScheme, toRomanNumeral, toChineseNumeral, toAlphaNumeral, computePageLabels };
    // 驗證用：目前每一頁的編號字串（分節編號）
    window.getPageLabels = () => {
        const entries = [];
        let section = 0;
        let inherited = null;
        for (const item of selectedPages) {
            if (!item) continue;
            if (item.type === 'divider') { section++; continue; }
            if (item.scheme) inherited = item.scheme;
            entries.push({ restartKey: `s${section}`, scheme: inherited });
        }
        return computePageLabels(entries);
    };
    window.getRawOrder = () => selectedPages.map(i => i.type === 'divider' ? `D:${i.firstLine}` : `P${i.pageNum}`);
    window.getSectionGroups = () => groupPagesBySection(selectedPages).map(g => ({
        title: g.title, pages: g.items.map(i => i.pageNum),
    }));
    // 驗證用：把最後一個項目（小節或頁面）搬到指定位置，等同拖曳
    window.moveLastItemTo = (index) => applyEdit('調整頁面順序', () => {
        if (selectedPages.length === 0) return;
        const last = selectedPages.pop();
        const target = Math.max(0, Math.min(index, selectedPages.length));
        selectedPages.splice(target, 0, last);
    });
    // 驗證用：取回最近一次拆檔的 ZIP（base64）
    window.getLastExportZip = () => {
        if (!lastExportZip) return null;
        let bin = '';
        for (let i = 0; i < lastExportZip.length; i++) bin += String.fromCharCode(lastExportZip[i]);
        return btoa(bin);
    };
    window.resetPageNumberSettings = resetPageNumberSettings;
    window.applyPageNumberPreset = applyPageNumberPreset;

    // PDF 生成與預覽
    window.undo = undo;
    window.redo = redo;

    // 目錄與設定
    window.generatePDF = generatePDF;
    window.downloadGeneratedPDF = downloadGeneratedPDF;
    window.closePreview = closePreview;

    // ------------------------------------------------------
    // 4b. 唯讀檢視 API（給自動化驗證用）
    // 說明：狀態都在這個閉包裡，外部只能透過 UI 操作；驗證需要斷言內部資料時
    // 用這組唯讀快照，回傳複本，改不動內部狀態。
    // ------------------------------------------------------
    window.getSourceOutline = () => {
        for (const file of pdfFiles) {
            if (file && file.outline && file.outline.length > 0) {
                return file.outline.map(e => ({ title: e.title, pageIndex: e.pageIndex, level: e.level }));
            }
        }
        return null;
    };
    window.getTocSnapshot = () => selectedPages
        .filter(p => p && p.type !== 'divider')
        .map(p => ({ title: p.firstLine || null, level: p.level || 0, pageNum: p.pageNum }));
    window.getHistoryState = () => ({
        undoDepth: undoStack.length,
        redoDepth: redoStack.length,
        undoLabels: undoStack.map(e => e.label),
        redoLabels: redoStack.map(e => e.label),
    });
    window.flushSessionSave = flushSessionSave;
    // 驗證用：內容邊界（裁白邊的依據）
    window.getContentBoxes = () => pdfFiles.map(f => (f.pages || []).map(p => p.contentBox));
    window.clearSession = clearSession;
    // 啟動時的「是否還原上次工作階段」檢查；呼叫端可以 await 它，
    // 避免還原對話框還沒問完就跟其他操作打架。
    window.sessionRestoreDone = null; // 由初始化階段填入

    // ------------------------------------------------------
    // 5. 事件監聽器綁定 (Event Listeners)
    // ------------------------------------------------------
    
    // 拖曳上傳
    const isPdfFile = (f) => f && (f.type === 'application/pdf' || /\.pdf$/i.test(f.name || ''));
    // 回傳本次接受的 PDF；非 PDF 一律提示，避免「丟了卻沒反應」
    function pickPdfFiles(fileListLike) {
        const all = Array.from(fileListLike || []);
        const pdfs = all.filter(isPdfFile);
        const skipped = all.length - pdfs.length;
        if (skipped > 0) showNotification(`⚠️ 已忽略 ${skipped} 個非 PDF 檔案`, 'info');
        return pdfs;
    }
    // 拖曳期間只有「確定是檔案」才顯示高亮，拖曳文字/連結不會誤亮
    const dragHasFiles = (e) => !!(e.dataTransfer && Array.from(e.dataTransfer.types || []).includes('Files'));

    uploadArea.addEventListener('dragover', (e) => {
        e.preventDefault();
        e.stopPropagation();
        if (dragHasFiles(e)) uploadArea.classList.add('drag-over');
    });
    uploadArea.addEventListener('dragleave', () => { uploadArea.classList.remove('drag-over'); });
    uploadArea.addEventListener('drop', (e) => {
        e.preventDefault();
        e.stopPropagation();
        uploadArea.classList.remove('drag-over');
        handleFiles(pickPdfFiles(e.dataTransfer.files));
    });
    fileInput.addEventListener('change', (e) => { handleFiles(pickPdfFiles(e.target.files)); });

    // 全視窗拖放：在視窗任意位置拖入 PDF 皆可載入
    window.addEventListener('dragover', (e) => {
        e.preventDefault();
        if (dragHasFiles(e)) uploadArea.classList.add('drag-over');
    });
    window.addEventListener('dragleave', (e) => {
        if (!e.relatedTarget) uploadArea.classList.remove('drag-over');
    });
    window.addEventListener('drop', (e) => {
        e.preventDefault();
        uploadArea.classList.remove('drag-over');
        handleFiles(pickPdfFiles(e.dataTransfer.files));
    });

    // Modal 點擊背景關閉 (dialog 的 backdrop 點擊 target 會是 dialog 本身)
    tocModal.addEventListener('click', (e) => { if (e.target === tocModal) tocModal.close(); });
    previewModal.addEventListener('click', (e) => { if (e.target === previewModal) previewModal.close(); });
    // Esc / close() 統一在此清理預覽資源
    previewModal.addEventListener('close', () => {
        document.getElementById('previewFrame').src = 'about:blank';
        if (currentPreviewUrl) {
            URL.revokeObjectURL(currentPreviewUrl);
            currentPreviewUrl = null;
        }
        finalPdfBytes = null;
    });

    // 復原／重做的鍵盤捷徑（在輸入框裡不要攔，否則會蓋掉打字undo）
    window.addEventListener('keydown', (e) => {
        const target = e.target;
        const typing = target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable);
        if (typing) return;
        const mod = e.metaKey || e.ctrlKey;
        if (!mod) return;
        const key = (e.key || '').toLowerCase();
        if (key === 'z' && !e.shiftKey) { e.preventDefault(); undo(); }
        else if ((key === 'z' && e.shiftKey) || key === 'y') { e.preventDefault(); redo(); }
    });

    // 關閉／重新整理前把工作階段寫下去（用同步的方式無法寫 IndexedDB，
    // 所以靠平時的 debounce 保存，這裡只補一次盡力而為的寫入）
    window.addEventListener('beforeunload', () => {
        // 只在「有還沒寫進去的變更」時才補寫。否則每次關頁都會發一個非同步寫入，
        // 可能在別的操作（例如清掉工作階段）之後才落地，把狀態蓋回去。
        if (sessionEnabled && sessionDirty && pdfFiles.length > 0) flushSessionSave();
    });

    // 目錄設定面板切換（目錄頁才需要字型設定；純書籤不需要）
    addTocCheckbox.addEventListener('change', function() {
        tocSettingsPanel.style.display = this.checked ? 'block' : 'none';
    });

    // 頁面標記面板切換（頁首／頁尾／頁碼共用同一個面板）
    const addMarksCheckbox = document.getElementById('addMarksCheckbox');
    if (addMarksCheckbox && marksSettingsPanel) {
        addMarksCheckbox.addEventListener('change', function() {
            marksSettingsPanel.style.display = this.checked ? 'block' : 'none';
        });
    }

    // 輸出設定卡可以整組收起來：720px 高度下七張卡會把上傳區擠出畫面，
    // 收起來後上傳／檔案清單／生成按鈕都還看得到。
    const settingsToggle = document.getElementById('settingsToggle');
    if (settingsToggle) {
        settingsToggle.addEventListener('click', () => {
            const collapsed = document.body.classList.toggle('settings-collapsed');
            settingsToggle.setAttribute('aria-expanded', String(!collapsed));
            settingsToggle.querySelector('.settings-toggle-text').textContent = collapsed ? '展開輸出設定' : '收合輸出設定';
        });
    }
    // 拆檔設定面板切換
    const enableSplitCheckbox = document.getElementById('enableSplitCheckbox');
    if (enableSplitCheckbox && splitSettingsPanel) {
        enableSplitCheckbox.addEventListener('change', function() {
            splitSettingsPanel.style.display = this.checked ? 'block' : 'none';
        });
    }

    // 拼版設定面板切換
    const enableImpositionCheckbox = document.getElementById('enableImpositionCheckbox');
    if (enableImpositionCheckbox && impositionSettingsPanel) {
        enableImpositionCheckbox.addEventListener('change', function() {
            impositionSettingsPanel.style.display = this.checked ? 'block' : 'none';
        });
    }

    // 版面設定面板切換
    const enableLayoutCheckbox = document.getElementById('enableLayoutCheckbox');
    if (enableLayoutCheckbox && layoutSettingsPanel) {
        enableLayoutCheckbox.addEventListener('change', function() {
            layoutSettingsPanel.style.display = this.checked ? 'block' : 'none';
        });
    }

    // 浮水印設定面板切換
    const addWatermarkCheckbox = document.getElementById('addWatermarkCheckbox');
    if (addWatermarkCheckbox && watermarkSettingsPanel) {
        addWatermarkCheckbox.addEventListener('change', function() {
            watermarkSettingsPanel.style.display = this.checked ? 'block' : 'none';
        });
    }

    // ------------------------------------------------------
    // 6. 初始化執行 (Initialization)
    // ------------------------------------------------------
    setViewMode(viewMode);   // 讓來源面板按鈕狀態與程式預設 (list) 一致
    setThumbnailSize('medium'); // 設定預設縮圖大小

    // 確保右側預設按鈕狀態正確
    setTargetViewMode(targetViewMode);
    setTargetThumbnailSize(targetThumbnailSize);

    if (addTocCheckbox.checked) {
        tocSettingsPanel.style.display = 'block';
    }
    setupDragAndDrop(); // Sortable 綁在容器上，初始化一次即可
    updateHistoryButtons();

    // 啟動時詢問是否還原上次的工作階段（沒有就什麼都不做）。
    // 存成 promise 讓呼叫端能等它問完，避免對話框跟其他操作打架。
    window.sessionRestoreDone = maybeRestoreSession();

    // 左右面板寬度調整
    (function initPanelResizer() {
        const resizer = document.getElementById('panelResizer');
        if (!resizer) return;
        resizer.addEventListener('pointerdown', (e) => {
            e.preventDefault();
            resizer.setPointerCapture(e.pointerId);
            resizer.classList.add('dragging');
            const onMove = (ev) => {
                const rect = sourcePanel.parentElement.getBoundingClientRect();
                const ratio = Math.min(0.8, Math.max(0.2, (ev.clientX - rect.left) / rect.width));
                sourcePanel.parentElement.style.gridTemplateColumns = `minmax(0, ${ratio}fr) 6px minmax(0, ${1 - ratio}fr)`;
            };
            const onUp = () => {
                resizer.classList.remove('dragging');
                resizer.removeEventListener('pointermove', onMove);
                resizer.removeEventListener('pointerup', onUp);
            };
            resizer.addEventListener('pointermove', onMove);
            resizer.addEventListener('pointerup', onUp);
        });
    })();

    // ======================================================
    // === 邏輯區塊：工具與通用函式 (Utilities)
    // ======================================================

    // 通用 <dialog> 輔助：取代會凍結頁面的 prompt()/confirm()
    function askDialog(title, { input = false, defaultValue = '' } = {}) {
        const dlg = document.getElementById('askDialog');
        const inputEl = document.getElementById('askDialogInput');
        document.getElementById('askDialogTitle').textContent = title;
        inputEl.style.display = input ? '' : 'none';
        inputEl.value = defaultValue;
        return new Promise(resolve => {
            const done = (result) => {
                dlg.close();
                document.getElementById('askDialogOk').onclick = null;
                document.getElementById('askDialogCancel').onclick = null;
                dlg.oncancel = null;
                resolve(result);
            };
            document.getElementById('askDialogOk').onclick = () => done(input ? inputEl.value : true);
            document.getElementById('askDialogCancel').onclick = () => done(null);
            dlg.oncancel = (e) => { e.preventDefault(); done(null); };
            // 已經開著就不要再 showModal()：會丟 InvalidStateError，呼叫端的 Promise 永遠不會 resolve。
            if (!dlg.open) dlg.showModal();
            if (input) inputEl.select();
        });
    }
    const askText = (title, defaultValue = '') => askDialog(title, { input: true, defaultValue });
    const askConfirm = (title) => askDialog(title);

    // 檔名與 PDF 內文抽出的標題會插入 innerHTML，必須跳脫
    function esc(str) {
        return String(str).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
    }

    function showNotification(message, type = 'error') {
        if (!notification) return;
        notification.textContent = message;
        notification.className = type; 
        notification.classList.add('show');
        setTimeout(() => { notification.classList.remove('show'); }, 3000);
    }

    function updateSelectedCountInfo() {
        let count = 0;
        pdfFiles.forEach(f => f.pages.forEach(p => { if(p.isChecked) count++; }));
        const info = document.getElementById('selectedCountInfo');
        if(info) info.textContent = `(已選 ${count} 頁)`;
    }

    function updateTargetSelectedInfo() {
        const count = selectedPages.filter(p => p.isChecked).length;
        const el = document.getElementById('targetSelectedCountInfo');
        if(el) el.textContent = `(${count})`;
    }

    // --- DOM 局部更新輔助（取代整面板重建，大量頁面時避免閃爍與卡頓） ---

    // 一次掃描建立「data-page-key → 節點」對照表：勾選/旋轉同步是 O(n) 次 querySelector，
    // 500 頁的文件每次點擊都要做 500 次選擇器查詢，這裡改成一次查詢 + Map 查找。
    function buildSourceKeyMap() {
        const map = new Map();
        sourcePages.querySelectorAll('[data-page-key]').forEach(el => {
            map.set(el.dataset.pageKey, el);
        });
        return map;
    }

    // 依資料狀態同步「左側來源」的勾選樣式/核取方塊
    function syncSourceCheckDom() {
        const map = buildSourceKeyMap();
        let missing = false;
        pdfFiles.forEach((file, fileIndex) => {
            if (!file) return;
            file.pages.forEach((page, pageIndex) => {
                const itemEl = map.get(`${fileIndex}_${pageIndex}`);
                if (itemEl) {
                    itemEl.classList.toggle('checked', !!page.isChecked);
                    const cb = itemEl.querySelector('.page-checkbox');
                    if (cb) cb.checked = !!page.isChecked;
                } else {
                    missing = true;
                }
            });
        });
        if (missing) renderSourcePages(); // 兜底：若結構不完整才重建
    }

    // 依資料狀態同步「右側成品」的勾選樣式/核取方塊
    function syncTargetCheckDom() {
        selectedPages.forEach((item, index) => {
            if (!item || item.type === 'divider') return;
            const itemEl = selectedPagesContainer.querySelector(`[data-index="${index}"]`);
            if (itemEl) {
                itemEl.classList.toggle('checked', !!item.isChecked);
                const cb = itemEl.querySelector('.page-checkbox');
                if (cb) cb.checked = !!item.isChecked;
            }
        });
    }

    // 只更新「左側來源」旋轉樣式（transition 保留自原 inline style）
    // 尚未渲染縮圖的骨架沒有 <img>，旋轉值會存在資料裡，等 fillSourceThumb 時一起套用。
    function applySourceRotationDom() {
        const map = buildSourceKeyMap();
        pdfFiles.forEach((file, fileIndex) => {
            if (!file) return;
            file.pages.forEach((page, pageIndex) => {
                const itemEl = map.get(`${fileIndex}_${pageIndex}`);
                const el = itemEl && itemEl.querySelector('.page-thumb-img, .thumb-fallback');
                if (el) el.style.transform = `rotate(${(page.sourceRotation || 0) % 360}deg)`;
            });
        });
    }

    // 只更新「右側成品」旋轉樣式
    function applyTargetRotationDom() {
        selectedPages.forEach((item, index) => {
            if (!item || item.type === 'divider') return;
            const itemEl = selectedPagesContainer.querySelector(`[data-index="${index}"]`);
            const el = itemEl && itemEl.querySelector('.page-thumb-img, .thumb-fallback');
            if (el) el.style.transform = `rotate(${(item.rotation || 0) % 360}deg)`;
        });
    }

    function getGlobalPageIndex(fileIndex, pageIndex) {
        let count = 0;
        for (let i = 0; i < fileIndex; i++) {
            if (pdfFiles[i]) count += pdfFiles[i].pages.length;
        }
        return count + pageIndex;
    }

    function getPageByGlobalIndex(globalIndex) {
        let count = 0;
        for (let i = 0; i < pdfFiles.length; i++) {
            const file = pdfFiles[i];
            if (globalIndex < count + file.pages.length) {
                return { fileIndex: i, pageIndex: globalIndex - count };
            }
            count += file.pages.length;
        }
        return null;
    }

    // ======================================================
    // === 邏輯區塊：檔案處理 (File Processing)
    // ======================================================

    // 縮圖存成 data URL：WebP 優先（同品質檔案最小），不支援時退回 JPEG。
    // 這裡只做一次編碼，之後畫面直接 <img>，不再需要離屏 canvas 與 drawImage 複製。
    // 注意：toDataURL 失敗時會回傳 'data:,'，直接塞進 <img> 會變成壞圖並噴 console error，
    // 所以編碼失敗一律回傳 null，讓 UI 走「無法預覽」的佔位。
    function isUsableDataUrl(url) {
        return typeof url === 'string' && /^data:image\//.test(url);
    }

    // 掃描畫布找出「非白」內容的邊界（像素座標）。用來支援「裁掉多餘白邊」：
    // 掃描件與公文常有大量白邊，逐頁量測比固定內縮準確得多。
    // 回傳的是「渲染後（含 /Rotate）」的座標；換算回頁面座標由呼叫端處理。
    function computeContentBBox(canvas, threshold = 245) {
        const w = canvas.width, h = canvas.height;
        if (w === 0 || h === 0) return null;
        let data;
        try { data = canvas.getContext('2d').getImageData(0, 0, w, h).data; } catch (e) { return null; }
        let minX = w, minY = h, maxX = -1, maxY = -1;
        for (let y = 0; y < h; y++) {
            const rowStart = y * w * 4;
            for (let x = 0; x < w; x++) {
                const o = rowStart + x * 4;
                const r = data[o], g = data[o + 1], b = data[o + 2], alpha = data[o + 3];
                // 透明算空白；只比亮度，避免彩色內容被判成空白
                if (alpha > 8 && (r < threshold || g < threshold || b < threshold)) {
                    if (x < minX) minX = x;
                    if (x > maxX) maxX = x;
                    if (y < minY) minY = y;
                    if (y > maxY) maxY = y;
                }
            }
        }
        if (maxX < 0 || maxY < 0) return null; // 整頁空白
        return { minX, minY, maxX, maxY, width: w, height: h };
    }

    function toThumbDataUrl(canvas) {
        try {
            const webp = canvas.toDataURL('image/webp', 0.6);
            if (isUsableDataUrl(webp)) return webp;
        } catch (e) { /* 不支援 WebP 就往下走 */ }
        try {
            const jpeg = canvas.toDataURL('image/jpeg', 0.7);
            return isUsableDataUrl(jpeg) ? jpeg : null;
        } catch (e) { return null; }
    }

    async function handleFiles(files) {
        if (!files || files.length === 0) {
            // 選擇檔案對話框被取消時不會有 change 事件，這裡僅是保險
            return;
        }
        if (isLoadingFiles) {
            // 載入中又丟檔案：排隊等這批跑完再處理。
            // （不能直接丟掉——使用者連續拖曳兩批、或程式化連續觸發時，第二批會憑空消失。）
            pendingFileQueue.push(...files);
            progress.textContent = `⏳ 正在載入中...（另有 ${pendingFileQueue.length} 個檔案排隊中）`;
            return;
        }
        isLoadingFiles = true;
        const loadedBefore = pdfFiles.length;
        const fileInputEl = document.getElementById('fileInput');
        const generateBtn = document.getElementById('generateBtn');
        if (fileInputEl) fileInputEl.disabled = true;
        if (generateBtn) generateBtn.disabled = true;

        const oversized = files.filter(f => f.size > MAX_FILE_BYTES);
        if (oversized.length > 0) {
            showNotification(`⚠️ ${oversized.length} 個檔案超過 ${Math.round(MAX_FILE_BYTES / 1024 / 1024)}MB，載入可能會很久`, 'info');
        }

        progress.textContent = `⏳ 正在載入 ${files.length} 個檔案...`;
        progress.classList.remove('success', 'error');
        progress.classList.add('active');

        let loadedCount = 0;
        const failedFiles = [];
        for (const [fileIdx, file] of files.entries()) {
            const fileData = { name: file.name, file: file, pages: [] };
            try {
                progress.textContent = `⏳ 正在載入檔案 ${fileIdx + 1}/${files.length}...`;
                const arrayBuffer = await file.arrayBuffer();
                const pdf = await pdfjsLib.getDocument({ data: arrayBuffer }).promise;
                progress.textContent = `⏳ 正在載入「${file.name}」(${pdf.numPages} 頁)...`;
                
                // 並行渲染縮圖（限制同時 4 個 worker，避免大檔案卡死 UI）
                const CONCURRENCY = 4;
                let nextPageNum = 1;
                let donePages = 0;
                async function renderPageWorker() {
                    while (nextPageNum <= pdf.numPages) {
                        const i = nextPageNum++;
                        try {
                            const page = await pdf.getPage(i);
                            const canvas = document.createElement('canvas');
                            const context = canvas.getContext('2d');
                            const viewport = page.getViewport({ scale: 0.5 });
                            canvas.width = viewport.width;
                            canvas.height = viewport.height;
                            await page.render({ canvasContext: context, viewport: viewport }).promise;
                            const info = await extractPageInfo(page, i);
                            // 縮圖存成壓縮後的 data URL，並立刻釋放 canvas 的點陣圖。
                            // 原本是「每個頁面留一張離屏 canvas，再 drawImage 複製一份到 DOM canvas」：
                            // A4 在 scale 0.5 是 298×421，一張約 0.5MB，200 頁就是 100MB（離屏）＋
                            // 100MB（DOM）＋右側再一份。實測 200 頁文件光 canvas 點陣圖就約 250MB，
                            // 500 頁的公文會直接把瀏覽器拖垮。改成 data URL 後同樣 200 頁只佔幾 MB。
                            // 量內容邊界要在縮圖編碼之前（編碼後就讀不到像素了）。
                            // viewport 用 scale 1 換算，才能拿回 pt 單位。
                            const viewAt1 = page.getViewport({ scale: 1 });
                            const rawBBox = computeContentBBox(canvas);
                            const contentBox = rawBBox ? {
                                minX: rawBBox.minX * 2, minY: rawBBox.minY * 2, // 畫布是 scale 0.5
                                maxX: (rawBBox.maxX + 1) * 2, maxY: (rawBBox.maxY + 1) * 2,
                                viewWidth: viewAt1.width, viewHeight: viewAt1.height,
                            } : null;
                            const thumb = toThumbDataUrl(canvas);
                            canvas.width = 0;
                            canvas.height = 0;
                            fileData.pages[i - 1] = { 
                                pageNum: i, 
                                thumb: thumb, 
                                firstLine: info.title,
                                isChecked: false, 
                                sourceRotation: 0,
                                // 內容邊界與原始尺寸，供「裁白邊」與「統一尺寸」使用
                                contentBox: contentBox,
                            };
                        } catch (pageErr) {
                            console.error(`處理 "${file.name}" 第 ${i} 頁失敗:`, pageErr);
                            // 保留佔位，避免頁碼索引錯亂
                            fileData.pages[i - 1] = { 
                                pageNum: i, 
                                thumb: null, 
                                firstLine: `Page ${i}`,
                                isChecked: false, 
                                sourceRotation: 0 
                            };
                        }
                        donePages++;
                        // 讓出主執行緒，維持 UI 反應
                        await new Promise(resolve => setTimeout(resolve, 0));
                        // 定期回報進度（避免每頁更新造成抖動）
                        if (donePages % 10 === 0 || donePages === pdf.numPages) {
                            progress.textContent = `⏳ 正在載入「${file.name}」... ${donePages}/${pdf.numPages} 頁`;
                        }
                    }
                }
                const workers = [];
                for (let w = 0; w < CONCURRENCY; w++) workers.push(renderPageWorker());
                await Promise.all(workers);

                // 縮圖已產生，趁文件還活著把書籤大綱抽出來（等等就要 destroy）
                fileData.outline = await extractOutline(pdf);
                const outlineLookup = buildOutlineLookup(fileData.outline);
                fileData.pages.forEach((page, pageIndex) => {
                    const hit = outlineLookup.get(pageIndex);
                    page.sourceTitle = hit ? hit.title : null;
                    page.sourceLevel = hit ? hit.level : 0;
                });

                await pdf.destroy(); // 縮圖已產生，釋放 pdf.js worker 記憶體；生成時會用 file 重新讀取
                if (fileData.outline.length > 0) sourceOutlines.set(fileData.name, outlineLookup);
                pdfFiles.push(fileData);
                loadedCount++;
            } catch (error) {
                console.error(`處理檔案 "${file.name}" 失敗:`, error);
                failedFiles.push(file.name);
                showNotification(`處理檔案 "${file.name}" 失敗，檔案可能已損毀。`, 'error');
            }
        }
        fileInput.value = ''; // 允許再次選取同一個檔案
        if (pdfFiles.length !== loadedBefore) {
            applyEdit('載入檔案', () => {}); // 只有真的載入成功才記一步歷史
        } else {
            updateFileList();
            renderSourcePages();
        }

        if (loadedCount === 0) {
            progress.textContent = '❌ 所有檔案載入失敗';
            progress.classList.add('error');
        } else {
            // 分批上傳時 loadedCount 只算這一批，累計數量才不會讓人以為前面的檔案掉了
            const total = pdfFiles.length;
            progress.textContent = files.length === loadedCount && files.length === total
                ? `✅ ${loadedCount} 個檔案載入完成！`
                : `✅ 本次載入 ${loadedCount}/${files.length} 個檔案（累計 ${total} 個）`;
            progress.classList.add('success');
        }
        setTimeout(() => { progress.classList.remove('active', 'success', 'error'); }, 2000);

        isLoadingFiles = false;
        if (fileInputEl) fileInputEl.disabled = false;
        if (generateBtn) generateBtn.disabled = false;
        if (failedFiles.length > 0) {
            showNotification(`⚠️ ${failedFiles.length} 個檔案載入失敗：${failedFiles.join('、')}`, 'error');
        }

        // 載入期間排隊的檔案接續處理
        if (pendingFileQueue.length > 0) {
            const queued = pendingFileQueue.splice(0, pendingFileQueue.length);
            handleFiles(queued);
        }
    }

    // ------------------------------------------------------
    // 頁碼格式
    // ------------------------------------------------------

    // 預設值集中一處，重設與首次載入都用同一份
    const PAGE_NUMBER_DEFAULTS = {
        format: '第 {n} 頁 / 共 {total} 頁',
        position: 'bottom-right',
        margin: 30,
        size: 10,
    };

    const PAGE_NUMBER_PRESETS = {
        plain: '{n}',
        chinese: '第 {n} 頁 / 共 {total} 頁',
        slash: '{n} / {total}',
        paren: '- {n} -',
    };

    function applyPageNumberPreset(key) {
        const preset = PAGE_NUMBER_PRESETS[key];
        if (!preset) return;
        document.getElementById('pageNumberFormat').value = preset;
        showNotification(`已套用格式：${preset}`, 'success');
    }

    function resetPageNumberSettings() {
        document.getElementById('headerFormat').value = '';
        document.getElementById('headerPosition').value = 'top-center';
        document.getElementById('footerFormat').value = '';
        document.getElementById('footerPosition').value = 'bottom-center';
        document.getElementById('pageNumberFormat').value = PAGE_NUMBER_DEFAULTS.format;
        document.getElementById('pageNumberPosition').value = PAGE_NUMBER_DEFAULTS.position;
        document.getElementById('pageNumberMargin').value = PAGE_NUMBER_DEFAULTS.margin;
        document.getElementById('pageNumberSize').value = PAGE_NUMBER_DEFAULTS.size;
        showNotification('✅ 標記設定已重設', 'success');
    }

    // 三種標記的定義：面板欄位 id、預設位置。三者共用邊距與字級。
    const MARK_DEFS = [
        { key: 'header', formatId: 'headerFormat', positionId: 'headerPosition', defaultPosition: 'top-center' },
        { key: 'footer', formatId: 'footerFormat', positionId: 'footerPosition', defaultPosition: 'bottom-center' },
        { key: 'pageNumber', formatId: 'pageNumberFormat', positionId: 'pageNumberPosition', defaultPosition: 'bottom-right' },
    ];

    // 讀出要畫的標記清單。格式留空＝不印；只有頁碼留空時退回「純數字」，
    // 因為使用者勾了「頁面標記」通常就是要頁碼。
    function readMarksConfig() {
        const margin = clampNumber(document.getElementById('pageNumberMargin').value, 8, 120, PAGE_NUMBER_DEFAULTS.margin);
        const size = clampNumber(document.getElementById('pageNumberSize').value, 6, 36, PAGE_NUMBER_DEFAULTS.size);
        const marks = [];
        for (const def of MARK_DEFS) {
            const raw = document.getElementById(def.formatId).value;
            let format = (typeof raw === 'string') ? raw.trim() : '';
            if (!format) {
                if (def.key !== 'pageNumber') continue; // 頁首／頁尾留空＝不印
                format = PAGE_NUMBER_PRESETS.plain;
            }
            marks.push({
                key: def.key,
                format,
                position: document.getElementById(def.positionId).value || def.defaultPosition,
                margin,
                size,
            });
        }
        return marks;
    }

    // ---- 編號方案（頁碼可以分節使用不同格式，例如正文用阿拉伯數字、附錄用羅馬數字）----
    const NUMBERING_SCHEMES = {
        decimal: '1, 2, 3',
        'roman-lower': 'i, ii, iii',
        'roman-upper': 'I, II, III',
        chinese: '一, 二, 三',
        'alpha-lower': 'a, b, c',
        'alpha-upper': 'A, B, C',
    };

    const ROMAN_TABLE = [[1000, 'M'], [900, 'CM'], [500, 'D'], [400, 'CD'], [100, 'C'], [90, 'XC'],
        [50, 'L'], [40, 'XL'], [10, 'X'], [9, 'IX'], [5, 'V'], [4, 'IV'], [1, 'I']];

    function toRomanNumeral(n) {
        if (!Number.isFinite(n) || n <= 0) return String(n);
        let out = '', v = Math.floor(n);
        for (const [val, sym] of ROMAN_TABLE) { while (v >= val) { out += sym; v -= val; } }
        return out;
    }

    // 中文數字（1..9999）：10 → 十、11 → 十一、101 → 一百〇一、2024 → 二千〇二十四
    function toChineseNumeral(n) {
        if (!Number.isFinite(n) || n <= 0 || n >= 10000) return String(n);
        const digits = '〇一二三四五六七八九';
        const units = ['', '十', '百', '千'];
        const str = String(Math.floor(n));
        let out = '';
        for (let i = 0; i < str.length; i++) {
            const digit = Number(str[i]);
            const unitIndex = str.length - 1 - i;
            if (digit === 0) {
                // 連續的 0 只補一個「〇」，結尾不留
                if (out !== '' && !out.endsWith('〇') && i < str.length - 1) out += '〇';
                continue;
            }
            // 十位數的 1 不加「一」（10 → 十、15 → 十五）
            if (!(digit === 1 && unitIndex === 1 && out === '')) out += digits[digit];
            out += units[unitIndex];
        }
        return out.replace(/〇+$/, '') || '〇';
    }

    function toAlphaNumeral(n, upper) {
        if (!Number.isFinite(n) || n <= 0) return String(n);
        let out = '', v = Math.floor(n);
        while (v > 0) {
            const r = (v - 1) % 26;
            out = String.fromCharCode((upper ? 65 : 97) + r) + out;
            v = Math.floor((v - 1) / 26);
        }
        return out;
    }

    function formatNumberByScheme(value, scheme) {
        switch (scheme) {
            case 'roman-lower': return toRomanNumeral(value).toLowerCase();
            case 'roman-upper': return toRomanNumeral(value);
            case 'chinese': return toChineseNumeral(value);
            case 'alpha-lower': return toAlphaNumeral(value, false);
            case 'alpha-upper': return toAlphaNumeral(value, true);
            default: return String(value);
        }
    }

    // 每一頁在「自己所屬群組」裡的編號字串。編號會在群組的第一頁重新起算，
    // 這樣「正文用 1,2,3、附錄重新用 i,ii,iii」才做得出來。
    function computePageLabels(entries) {
        const labels = [];
        let previousKey = null;
        let counter = 0;
        for (const entry of entries) {
            const key = entry.restartKey === undefined ? null : entry.restartKey;
            if (key !== previousKey) { counter = 0; previousKey = key; }
            counter++;
            labels.push(formatNumberByScheme(counter, entry.scheme));
        }
        return labels;
    }

    // {n} 目前頁碼、{total} 總頁數、{name} 來源檔名、{date} 日期。
    // 其他大括號內容原樣保留（例如想印「{附件}」不會被吃掉）。
    function formatPageNumber(template, values) {
        return String(template).replace(/\{(n|total|name|date)\}/g, (match, key) => {
            const v = values[key];
            return (v === undefined || v === null) ? match : String(v);
        });
    }

    function pageNumberPositionXY(position, pageWidth, pageHeight, textWidth, margin) {
        const vertical = String(position || '').startsWith('top') ? 'top' : 'bottom';
        let horizontal = 'right';
        if (position === 'bottom-center' || position === 'top-center') horizontal = 'center';
        else if (position === 'bottom-left' || position === 'top-left') horizontal = 'left';

        let x = pageWidth - margin - textWidth;
        if (horizontal === 'center') x = (pageWidth - textWidth) / 2;
        else if (horizontal === 'left') x = margin;
        const y = vertical === 'top' ? pageHeight - margin : margin;
        return { x: Math.max(0, x), y: Math.max(0, y) };
    }

    // ------------------------------------------------------
    // 版面：裁掉白邊 / 內縮 / 統一頁面尺寸
    // ------------------------------------------------------

    const UNIFORM_SIZES = {
        a4: [595.28, 841.89],
        a5: [419.53, 595.28],
        b5: [498.9, 708.66],
        letter: [612, 792],
    };

    // 把「渲染後（含 /Rotate）的內容邊界」換算成未旋轉頁面座標。
    // 邊界是在渲染後的視圖空間量的，而 MediaBox/CropBox 用的是頁面自己的座標系，
    // 所以要先反轉 /Rotate 的映射，否則有旋轉的頁面會裁錯位置。
    function viewBoxToPageBox(box, rotate, pageWidth, pageHeight) {
        const angle = ((rotate % 360) + 360) % 360;
        const vw = box.viewWidth, vh = box.viewHeight;
        const x0 = box.minX, y0 = box.minY, x1 = box.maxX, y1 = box.maxY;
        switch (angle) {
            case 90:
                // 視圖為 (x,y) 時，對應未旋轉頁面的 (y, vw-x)
                return { left: y0, right: y1, bottom: vw - x1, top: vw - x0 };
            case 180:
                return { left: vw - x1, right: vw - x0, bottom: y0, top: y1 };
            case 270:
                return { left: vh - y1, right: vh - y0, bottom: x0, top: x1 };
            default:
                // 未旋轉：視圖與頁面同寬高，y 要翻轉（canvas 上方原點、頁面下方原點）
                return { left: x0, right: x1, bottom: vh - y1, top: vh - y0 };
        }
    }

    function resetLayoutSettings() {
        document.getElementById('layoutFitSelect').value = 'original';
        document.getElementById('layoutMarginInput').value = 10;
        document.getElementById('uniformSizeSelect').value = 'original';
        showNotification('✅ 版面設定已重設', 'success');
    }

    function readLayoutConfig() {
        // 沒勾「版面調整」就完全不動頁面框與尺寸
        const enabled = !!(document.getElementById('enableLayoutCheckbox') || {}).checked;
        if (!enabled) return { fit: 'original', margin: 0, targetSize: null };
        const fit = document.getElementById('layoutFitSelect').value || 'original';
        const uniform = document.getElementById('uniformSizeSelect').value || 'original';
        return {
            fit,
            margin: clampNumber(document.getElementById('layoutMarginInput').value, 0, 200, 10),
            // 'original' 以外都是統一尺寸
            targetSize: uniform === 'original' ? null : (UNIFORM_SIZES[uniform] || null),
        };
    }

    // 依設定把 MediaBox/CropBox 縮到要保留的範圍。
    // 這只改「頁面框」，不動內容，所以文字、搜尋、書籤連結全部不受影響。
    function applyCropBox(page, item, config) {
        const { width, height } = page.getSize();
        if (!(width > 0 && height > 0)) return;

        let box = null;
        if (config.fit === 'content') {
            const cb = item.contentBox;
            const rotate = page.getRotation().angle;
            // 邊界是在「加入右側當下」量的；使用者之後若又旋轉這頁，兩者會不一致，
            // 這種情況直接跳過，避免裁到不該裁的位置。
            const addedRotation = item.rotation || 0;
            if (cb && ((rotate - addedRotation) % 360 + 360) % 360 === 0) {
                box = viewBoxToPageBox(cb, rotate - addedRotation, width, height); // 邊界是在原始 /Rotate 下量的
            }
        } else if (config.fit === 'inset') {
            const m = config.margin;
            box = { left: m, bottom: m, right: width - m, top: height - m };
        }
        if (!box) return;

        const left = Math.max(0, Math.min(box.left - config.margin, width - 1));
        const right = Math.max(left + 1, Math.min(box.right + config.margin, width));
        const bottom = Math.max(0, Math.min(box.bottom - config.margin, height - 1));
        const top = Math.max(bottom + 1, Math.min(box.top + config.margin, height));
        try {
            page.setMediaBox(left, bottom, right - left, top - bottom);
            page.setCropBox(left, bottom, right - left, top - bottom);
        } catch (cropError) {
            console.error('裁切失敗：', cropError);
        }
    }

    // 把頁面統一到目標尺寸：改頁面框 + 在內容前面補一個縮放／平移矩陣。
    //
    // 為什麼不用 pdf-lib 的 embedPage + drawPage（最直覺的作法）：
    // 這兩者與 CropBox 會互相干扰。實測結果（同一份內容頁）：
    //   - embed 後 removePage：XObject 內容被清掉，整頁空白且不報錯。
    //   - 保留原頁面但同時縮放：文字抽得到、渲染卻沒有墨點（等於空白頁）。
    //   - 只有「不裁切 + 不縮放」的組合才會正常。
    // 改用在內容流前面補 `q / cm ... / Q`：文字、向量、圖片全部照舊，
    // 而且不必重建文件或搬動頁面，原生的頁面結構也保留著。
    function scalePageToSize(page, targetSize, concatMatrix, pushGs, popGs) {
        const [tw, th] = targetSize;
        const box = page.getMediaBox();
        if (!(box.width > 0 && box.height > 0)) return;
        const scale = Math.min(tw / box.width, th / box.height);
        const offsetX = (tw - box.width * scale) / 2;
        const offsetY = (th - box.height * scale) / 2;

        // 內容座標的原點在 (box.x, box.y)，要先平移回原點再縮放，最後放到新頁的位置
        const matrix = concatMatrix(scale, 0, 0, scale, offsetX - box.x * scale, offsetY - box.y * scale);
        try {
            page.node.wrapContentStreams(
                page.doc.context.register(page.doc.context.obj(`q\n${matrix.toString()}`)),
                page.doc.context.register(page.doc.context.obj(`Q`)),
            );
        } catch (wrapError) {
            console.error('插入縮放矩陣失敗：', wrapError);
            return;
        }
        page.setMediaBox(0, 0, tw, th);
        page.setCropBox(0, 0, tw, th);
    }

    // ------------------------------------------------------
    // 依小節拆成多檔（打包成 ZIP 下載）
    // ------------------------------------------------------

    // 只做 STORE（不壓縮）的 ZIP：PDF 本身已經壓縮過，再壓收益很低，
    // 而 STORE 的格式簡單到可以自己寫，不必為了打包多帶一個函式庫進來。
    function crc32(bytes) {
        if (!crc32.table) {
            const table = new Uint32Array(256);
            for (let n = 0; n < 256; n++) {
                let c = n;
                for (let k = 0; k < 8; k++) c = (c & 1) ? (0xEDB88320 ^ (c >>> 1)) : (c >>> 1);
                table[n] = c >>> 0;
            }
            crc32.table = table;
        }
        const table = crc32.table;
        let crc = 0xFFFFFFFF;
        for (let i = 0; i < bytes.length; i++) crc = table[(crc ^ bytes[i]) & 0xFF] ^ (crc >>> 8);
        return (crc ^ 0xFFFFFFFF) >>> 0;
    }

    // files: [{ name, data: Uint8Array }]
    function buildZip(files) {
        const enc = new TextEncoder();
        const chunks = [];
        const central = [];
        let offset = 0;
        for (const file of files) {
            const nameBytes = enc.encode(file.name);
            const crc = crc32(file.data);
            const size = file.data.length;
            const local = new Uint8Array(30 + nameBytes.length);
            const lv = new DataView(local.buffer);
            lv.setUint32(0, 0x04034b50, true);   // local file header
            lv.setUint16(4, 20, true);           // version needed
            lv.setUint16(6, 0x0800, true);       // bit 11：檔名是 UTF-8
            lv.setUint16(8, 0, true);            // STORE
            lv.setUint32(14, crc, true);
            lv.setUint32(18, size, true);
            lv.setUint32(22, size, true);
            lv.setUint16(26, nameBytes.length, true);
            local.set(nameBytes, 30);
            chunks.push(local, file.data);
            central.push({ nameBytes, crc, size, offset });
            offset += local.length + size;
        }
        const centralStart = offset;
        for (const e of central) {
            const rec = new Uint8Array(46 + e.nameBytes.length);
            const cv = new DataView(rec.buffer);
            cv.setUint32(0, 0x02014b50, true);   // central directory header
            cv.setUint16(4, 20, true);
            cv.setUint16(6, 20, true);
            cv.setUint16(8, 0x0800, true);
            cv.setUint16(10, 0, true);
            cv.setUint32(16, e.crc, true);
            cv.setUint32(20, e.size, true);
            cv.setUint32(24, e.size, true);
            cv.setUint16(28, e.nameBytes.length, true);
            cv.setUint32(42, e.offset, true);
            rec.set(e.nameBytes, 46);
            chunks.push(rec);
            offset += rec.length;
        }
        const eocd = new Uint8Array(22);
        const ev = new DataView(eocd.buffer);
        ev.setUint32(0, 0x06054b50, true);       // end of central directory
        ev.setUint16(8, central.length, true);
        ev.setUint16(10, central.length, true);
        ev.setUint32(12, offset - centralStart, true);
        ev.setUint32(16, centralStart, true);
        chunks.push(eocd);

        const total = chunks.reduce((n, c) => n + c.length, 0);
        const out = new Uint8Array(total);
        let pos = 0;
        for (const c of chunks) { out.set(c, pos); pos += c.length; }
        return out;
    }

    // 觸發瀏覽器下載（統一的 blob 下載路徑，ZIP 與單檔共用）
    function downloadBlob(blob, fileName) {
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = fileName;
        a.style.display = 'none';
        document.body.appendChild(a);
        a.click();
        setTimeout(() => {
            try { document.body.removeChild(a); URL.revokeObjectURL(url); } catch (e) { /* 已清理 */ }
        }, 100);
    }

    // 檔名不能含路徑分隔字元；Windows／macOS 的保留字也一併換掉
    function sanitizeFileName(name) {
        return String(name || '')
            .replace(/[\\/:*?"<>|]/g, '_')
            .replace(/[\x00-\x1f]/g, '')
            .replace(/\s+/g, ' ')
            .trim()
            .slice(0, 80) || 'untitled';
    }

    function readSplitConfig() {
        const enabled = !!(document.getElementById('enableSplitCheckbox') || {}).checked;
        if (!enabled) return { enabled: false };
        const raw = document.getElementById('splitNameFormat').value;
        const padWidth = clampNumber(document.getElementById('splitPadWidth').value, 1, 4, 2);
        return {
            enabled: true,
            format: (typeof raw === 'string' && raw.trim()) ? raw.trim() : '{title}',
            padWidth: Math.round(padWidth),
        };
    }

    function resetSplitSettings() {
        document.getElementById('splitNameFormat').value = '{title}';
        document.getElementById('splitPadWidth').value = 2;
        showNotification('✅ 拆檔設定已重設', 'success');
    }

    // 依小節把成品分組：小節標題之後的頁面都屬於那個小節；開頭沒有小節的頁面自成一群。
    function groupPagesBySection(items) {
        const groups = [];
        let current = null;
        for (const item of items) {
            if (!item) continue;
            if (item.type === 'divider') {
                current = { title: item.firstLine || '未命名小節', items: [] };
                groups.push(current);
                continue;
            }
            if (!current) {
                current = { title: '', items: [] };
                groups.push(current);
            }
            current.items.push(item);
        }
        return groups.filter(g => g.items.length > 0);
    }

    // 產生一組檔名：套用格式、去重、補編號
    function buildSplitFileNames(groups, config, dateText) {
        const used = new Set();
        const total = groups.length;
        return groups.map((group, index) => {
            const n = String(index + 1).padStart(config.padWidth, '0');
            const title = group.title || `第${n}部分`;
            let name = config.format
                .replace(/\{n\}/g, n)
                .replace(/\{title\}/g, title)
                .replace(/\{total\}/g, String(total))
                .replace(/\{date\}/g, dateText);
            name = sanitizeFileName(name);
            if (!/\.pdf$/i.test(name)) name += '.pdf';
            // 同名時補流水號，避免覆蓋
            let candidate = name;
            let suffix = 2;
            while (used.has(candidate.toLowerCase())) {
                candidate = name.replace(/\.pdf$/i, `_${suffix}.pdf`);
                suffix++;
            }
            used.add(candidate.toLowerCase());
            return candidate;
        });
    }

    // 拆檔時為「一個小節」產生一份 PDF。只套用直接作用在頁面上的設定
    // （裁切、統一尺寸、頁碼／頁首／頁尾），不含目錄頁與書籤大綱 ——
    // 那些是「整份文件」層級的東西，逐檔重複產生反而奇怪。
    async function buildSplitPdf(sectionItems, deps) {
        const { PDFDocument, rgb, StandardFonts, degrees } = PDFLib;
        const { layoutConfig, marksConfig, splitIndex, splitTotal, dateText, customFont, asciiFont, cjkFont, cjkFontAvailable } = deps;
        const out = await PDFDocument.create();
        const cache = new Map();
        const entries = [];

        for (const item of sectionItems) {
            if (item.type === 'blank') {
                const prev = entries[entries.length - 1];
                entries.push({ item, page: addBlankPdfPage(out, prev && prev.page, item.rotation) });
                continue;
            }
            const sourceFile = pdfFiles[item.fileIndex];
            if (!sourceFile || !sourceFile.file || !item.pageNum) continue;
            try {
                let sourcePdf = cache.get(item.fileIndex);
                if (!sourcePdf) {
                    sourcePdf = await PDFDocument.load(await sourceFile.file.arrayBuffer(), {
                        ignoreEncryption: true, updateMetadata: false, throwOnInvalidObject: false,
                    });
                    cache.set(item.fileIndex, sourcePdf);
                }
                if (item.pageNum < 1 || item.pageNum > sourcePdf.getPageCount()) continue;
                const [copied] = await out.copyPages(sourcePdf, [item.pageNum - 1]);
                const page = out.addPage(copied);
                page.setRotation(degrees((copied.getRotation().angle + (item.rotation || 0)) % 360));
                if (layoutConfig.fit !== 'original') applyCropBox(page, item, layoutConfig);
                if (layoutConfig.targetSize) {
                    scalePageToSize(page, layoutConfig.targetSize, PDFLib.concatTransformationMatrix,
                        PDFLib.pushGraphicsState, PDFLib.popGraphicsState);
                }
                entries.push({ item, page });
            } catch (error) {
                console.error(`拆檔時處理「${sourceFile.name}」第 ${item.pageNum} 頁失敗：`, error);
            }
        }

        // 頁面標記（頁首／頁尾／頁碼）
        if (marksConfig.length > 0) {
            const totalPages = entries.length;
            entries.forEach(({ page, item }, index) => {
                const { width, height } = page.getSize();
                if (!(width > 0 && height > 0)) return;
                for (const mark of marksConfig) {
                    const label = formatPageNumber(mark.format, {
                        n: index + 1,
                        total: totalPages,
                        name: item.fileName || '',
                        date: dateText,
                        section: splitIndex,
                        sections: splitTotal,
                    });
                    if (!label) continue;
                    const textWidth = watermarkTextWidth(label, mark.size, cjkFont, asciiFont);
                    const { x, y } = pageNumberPositionXY(mark.position, width, height, textWidth, mark.margin);
                    try {
                        drawMixedText(page, label, x, y, mark.size, cjkFont, asciiFont, rgb(0, 0, 0), { cjkFontAvailable });
                    } catch (e) { /* 單一標記失敗不影響整份輸出 */ }
                }
            });
        }
        return out.save();
    }

    // ------------------------------------------------------
    // 一頁多張／騎馬釘拼版
    // ------------------------------------------------------

    // 註：buildSaddleOrder 是純函式，介面足夠小，所以直接斷言它的輸出
    //（verify.mjs 有單元檢查）。純函式用單元測試比透過產出 PDF 反推可靠得多。

    const SHEET_SIZES = {
        a4: [595.28, 841.89],
        a5: [419.53, 595.28],
        letter: [612, 792],
    };

    // 每張要排幾頁、以及格子是幾欄幾列
    function gridForNUp(n) {
        if (n >= 9) return { cols: 3, rows: 3 };
        if (n >= 6) return { cols: 3, rows: 2 };
        if (n >= 4) return { cols: 2, rows: 2 };
        if (n >= 2) return { cols: 2, rows: 1 };
        return { cols: 1, rows: 1 };
    }

    // 騎馬釘的頁序：n 頁（4 的倍數）→ 依序要放到每一格的是「第幾頁」。
    // 標準作法是「外層配內層」，對折後頁碼才會連續：
    //   4 頁 → [4,1, 2,3]        第 1 張：P4|P1，第 2 張：P2|P3
    //   8 頁 → [8,1, 2,7, 6,3, 4,5]
    // 回傳 0 起算的索引（呼叫端直接拿去查陣列，不要再自行加減一）。
    // 騎馬釘拼版順序（純函式：輸入頁數、回傳每一格該放第幾頁，0 起算）。
    // 一張紙有兩面、每面兩格，所以 n 頁需要 n/4 張紙；k 為紙張編號（0 起算）：
    //   正面： (n-2k) | (2k+1)
    //   背面： (2k+2) | (n-1-2k)
    // 例：n=4 → [4|1, 2|3]；n=8 → [8|1, 2|7, 6|3, 4|5]
    // 對折後依序攤開即為 1,2,3...n。n 必須是 4 的倍數。
    function buildSaddleOrder(pageCount) {
        const out = [];
        for (let k = 0; k < pageCount / 4; k++) {
            out.push(pageCount - 2 * k);      // 正面左
            out.push(2 * k + 1);              // 正面右
            out.push(2 * k + 2);              // 背面左
            out.push(pageCount - 1 - 2 * k);  // 背面右
        }
        return out.map(pageNo => pageNo - 1);
    }

    function readImpositionConfig() {
        const enabled = !!(document.getElementById('enableImpositionCheckbox') || {}).checked;
        if (!enabled) return { enabled: false, n: 1, saddle: false, border: false, order: 'row', sheetSize: null };
        const rawN = parseInt(document.getElementById('impositionNUpSelect').value, 10);
        const n = [1, 2, 4, 6, 9].includes(rawN) ? rawN : 1;
        const sheetKey = document.getElementById('impositionSheetSizeSelect').value;
        return {
            enabled: n > 1,
            n,
            saddle: !!document.getElementById('impositionSaddleCheckbox').checked,
            border: !!document.getElementById('impositionBorderCheckbox').checked,
            order: document.getElementById('impositionOrderSelect').value === 'column' ? 'column' : 'row',
            sheetSize: sheetKey === 'original' ? null : (SHEET_SIZES[sheetKey] || null),
        };
    }

    function resetImpositionSettings() {
        document.getElementById('impositionNUpSelect').value = '2';
        document.getElementById('impositionSheetSizeSelect').value = 'original';
        document.getElementById('impositionOrderSelect').value = 'row';
        document.getElementById('impositionSaddleCheckbox').checked = false;
        document.getElementById('impositionBorderCheckbox').checked = false;
        showNotification('✅ 拼版設定已重設', 'success');
    }

    // 把 contentEntries 重新排成拼版後的單張頁面，並移除原本的頁面。
    // 回傳新的 contentEntries（每一項代表「一張紙」）。
    async function buildImposition(pdfDoc, contentEntries, config, ctx) {
        const { rgb, PDFDocument } = ctx;
        const n = config.n;
        // 騎馬釘固定每面 2 格（buildSaddleOrder 的頁序就是以每面 2 頁排的）
        const { cols, rows } = config.saddle ? { cols: 2, rows: 1 } : gridForNUp(n);

        // 把要拼版的頁面先複製到一個「暫存文件」，之後一律從那裡 embedPage。
        // 原因：pdf-lib 的 removePage() 會把同文件內已建立的 XObject 內容一起清掉
        // （實測：拼完再刪原頁面 → 整張紙空白且不報錯）。改成跨文件 embed，
        // 原頁面被刪掉時就不會影響暫存文件裡的來源，XObject 也還有效。
        const firstSize = contentEntries[0] && contentEntries[0].page
            ? contentEntries[0].page.getSize() : { width: 595.28, height: 841.89 };
        const scratch = await PDFDocument.create();
        // pdf-lib 沒有 getPageIndex()，用 getPages() 的位置自己找
        const allPages = pdfDoc.getPages();
        const copied = await scratch.copyPages(pdfDoc, contentEntries.map(entry => allPages.indexOf(entry.page)));

        // 決定每一格要放第幾頁（索引指向 contentEntries；-1 表示留白）
        const cellsPerSide = cols * rows;
        // 騎馬釘時每一張紙有「正、反」兩面，所以需要的格數是一般拼版的兩倍
        const cellsPerSheet = cellsPerSide * (config.saddle ? 2 : 1);

        let slots = [];
        if (config.saddle) {
            // buildSaddleOrder 回傳的是「每一格該放第幾頁」，且已經按
            // 「第 1 張正面、第 1 張背面、第 2 張正面...」的順序排好（每面 cellsPerSide 格）；
            // 不足的頁數以 -1 補成 4 的倍數（空白頁）。
            let totalPages = contentEntries.length;
            while (totalPages % 4 !== 0) totalPages++;
            slots = buildSaddleOrder(totalPages);
            // 頁數不足時，補出來的空白頁索引會超出 contentEntries，畫的時候跳過即可
        } else {
            for (let i = 0; i < contentEntries.length; i++) slots.push(i);
        }

        // 補到每張紙剛好的格數
        while (slots.length % cellsPerSheet !== 0) slots.push(-1);

        const originals = contentEntries.map(e => e.page);
        const sheetEntries = [];

        // 每一「面」輸出成一頁 PDF。騎馬釘一張紙有正反兩面，雙面列印後對折；
        // 其他模式一張紙只有一面。slots 已按「第 1 面、第 2 面...」依序排好。
        const first = originals[0];
        const [sw, sh] = config.sheetSize || (first ? [first.getSize().width, first.getSize().height] : [595.28, 841.89]);
        const cellW = sw / cols;
        const cellH = sh / rows;
        const faceCount = slots.length / cellsPerSide;

        for (let f = 0; f < faceCount; f++) {
            const sheet = pdfDoc.addPage([sw, sh]);
            let representative = null;
            for (let k = 0; k < cellsPerSide; k++) {
                const entryIndex = slots[f * cellsPerSide + k];
                // 依排列方向決定這一格在哪一欄哪一列
                const col = config.order === 'column' ? Math.floor(k / rows) : (k % cols);
                const row = config.order === 'column' ? (k % rows) : Math.floor(k / cols);
                const cellLeft = col * cellW;
                const cellTop = sh - (row + 1) * cellH; // PDF 的原點在左下

                if (config.border) {
                    sheet.drawRectangle({
                        x: cellLeft, y: cellTop, width: cellW, height: cellH,
                        borderColor: rgb(0.75, 0.75, 0.78), borderWidth: 0.5,
                    });
                }
                if (entryIndex < 0 || entryIndex >= copied.length) continue; // 留白

                // 這一面第一個真的有頁面的項目當代表（大綱／頁碼會用到它的標題），
                // 並記下每個原始頁落在第幾頁，目錄／書籤／頁碼要用它對回拼版後的頁
                const entry = contentEntries[entryIndex];
                if (!representative) representative = entry.item;
                if (entry.sheetIndex === undefined) entry.sheetIndex = sheetEntries.length;

                const sourcePage = copied[entryIndex];
                if (!sourcePage || typeof sourcePage.getSize !== 'function') continue;
                const size = sourcePage.getSize();
                if (!(size.width > 0 && size.height > 0)) continue;
                // 插入的空白頁沒有內容流，embedPage 會在存檔時才失敗；直接留白格
                if (!sourcePage.node.Contents()) continue;
                const embedded = await pdfDoc.embedPage(sourcePage);
                const scale = Math.min(cellW / size.width, cellH / size.height);
                const w = size.width * scale, h = size.height * scale;
                sheet.drawPage(embedded, {
                    x: cellLeft + (cellW - w) / 2,
                    y: cellTop + (cellH - h) / 2,
                    xScale: scale, yScale: scale,
                });
            }
            // 整面都是補出來的空白時，給一個安全的最小物件，避免後面讀 item.pageNum 爆掉
            sheetEntries.push({
                page: sheet,
                item: representative || { pageNum: null, fileName: '', firstLine: '' },
            });
        }

        // 移除原本的頁面：務必在所有 embedPage 完成之後才做（removePage 會清掉 XObject）。
        // 由後往前刪，索引才不會位移。
        for (let i = originals.length - 1; i >= 0; i--) {
            const idx = pdfDoc.getPages().indexOf(originals[i]);
            if (idx >= 0) pdfDoc.removePage(idx);
        }
        return sheetEntries;
    }

    // ------------------------------------------------------
    // 浮水印／印章
    // ------------------------------------------------------

    const WATERMARK_COLORS = {
        red: [0.85, 0.10, 0.15],
        gray: [0.45, 0.45, 0.48],
        blue: [0.10, 0.35, 0.80],
        black: [0.10, 0.10, 0.10],
    };

    // 頁面範圍：支援 1-3,5,7- 這種寫法；空字串＝全部
    function parsePageRange(spec, total) {
        const all = new Set();
        for (let i = 1; i <= total; i++) all.add(i);
        const text = String(spec || '').trim();
        if (!text) return all;

        const picked = new Set();
        for (const rawPart of text.split(',')) {
            const part = rawPart.trim().replace(/[~～—]/g, '-');
            if (!part) continue;
            const range = part.match(/^(\d+)?\s*-\s*(\d+)?$/);
            if (range) {
                const start = range[1] ? parseInt(range[1], 10) : 1;
                const end = range[2] ? parseInt(range[2], 10) : total;
                const lo = Math.max(1, Math.min(start, end));
                const hi = Math.min(total, Math.max(start, end));
                for (let i = lo; i <= hi; i++) picked.add(i);
                continue;
            }
            const single = parseInt(part, 10);
            if (Number.isFinite(single) && single >= 1 && single <= total) picked.add(single);
        }
        return picked;
    }

    // 數字欄位：夾在合理區間，避免有人填 0 或負數
    const clampNumber = (value, min, max, fallback) => {
        const n = parseFloat(value);
        if (!Number.isFinite(n)) return fallback;
        return Math.min(max, Math.max(min, n));
    };

    function readWatermarkConfig() {
        const text = (document.getElementById('watermarkText').value || '').trim();
        const colorKey = document.getElementById('watermarkColor').value;
        return {
            text,
            // 空字串代表「不蓋浮水印」，不是「蓋空白」
            enabled: !!text,
            layout: document.getElementById('watermarkLayout').value === 'tile' ? 'tile' : 'center',
            // 只做 -90~90 度：180 度會變成上下顛倒的字，實務上沒人這樣蓋章
            angle: clampNumber(document.getElementById('watermarkAngle').value, -90, 90, 45),
            size: clampNumber(document.getElementById('watermarkSize').value, 8, 200, 40),
            opacity: clampNumber(document.getElementById('watermarkOpacity').value, 5, 100, 18) / 100,
            color: WATERMARK_COLORS[colorKey] || WATERMARK_COLORS.red,
        };
    }

    // 文字寬度：ASCII 走 Helvetica、中文走 CJK 字型，跟目錄同一套規則
    function watermarkTextWidth(text, size, cjkFont, asciiFont) {
        return splitMixedRuns(text).reduce((w, run) => {
            const font = run.ascii ? asciiFont : cjkFont;
            try { return w + font.widthOfTextAtSize(run.s, size); } catch (e) { return w; }
        }, 0);
    }

    // 同一個 ExtGState 名稱可以在不同頁面重複使用（Resources 是每頁各自的），
    // 只有同一頁要蓋兩次時才需要換名字，否則會多出用不到的資源。
    function findOrCreateExtGState(page, name, dict) {
        const context = page.doc.context;
        const resources = page.node.Resources();
        if (resources) {
            const existing = resources.lookup(globalThis.PDFLib.PDFName.of('ExtGState'));
            if (existing && existing.has && existing.has(globalThis.PDFLib.PDFName.of(name))) {
                return globalThis.PDFLib.PDFName.of(name);
            }
        }
        return page.node.newExtGState(name, context.obj(dict));
    }

    // rgb 由呼叫端提供（PDFLib 的解構在 generatePDF 裡，模組層級拿不到）
    function drawWatermarkOnPage(page, config, fonts) {
        const wmColor = fonts.rgb(config.color[0], config.color[1], config.color[2]);
        if (config.layout === 'tile') {
            drawTiledWatermark(page, config, fonts, wmColor);
            return;
        }
        const { width, height } = page.getSize();
        drawMixedText(page, config.text, width / 2, height / 2, config.size, fonts.centerFont, fonts.asciiFont,
            wmColor, { opacity: config.opacity, rotate: fonts.degrees(config.angle), centerX: true });
    }

    function drawTiledWatermark(page, config, fonts, wmColor) {
        const { width, height } = page.getSize();
        const center = fonts.centerFont;
        const wmWidth = Math.max(1, watermarkTextWidth(config.text, config.size, center, fonts.asciiFont));
        const wmHeight = config.size;
        // 間距要用「旋轉後的包圍盒」算：45 度時旋轉會把高度吃掉一大半，
        // 只用原始字高算會讓每一列的實際留白愈來愈大（實測會上下各空掉一大塊）。
        const rad = Math.abs(config.angle) * Math.PI / 180;
        const cos = Math.cos(rad), sin = Math.sin(rad);
        const boxW = wmWidth * cos + wmHeight * sin;
        const boxH = wmWidth * sin + wmHeight * cos;
        const gapX = boxW + config.size * 0.9;
        const gapY = boxH + config.size * 1.6;
        const cols = Math.max(1, Math.floor((width + gapX * 0.3) / gapX));
        const rows = Math.max(1, Math.floor((height + gapY * 0.3) / gapY));
        if (cols < 1 || rows < 1) {
            // 字太大、頁太小：退成單一置中，總比畫不出來好
            drawMixedText(page, config.text, width / 2, height / 2, config.size, center, fonts.asciiFont,
                wmColor, { opacity: config.opacity, rotate: fonts.degrees(config.angle), centerX: true });
            return;
        }
        const stepX = width / cols;
        const stepY = height / rows;
        for (let row = 0; row < rows; row++) {
            for (let col = 0; col < cols; col++) {
                // 棋盤式交錯，避免每一列都對齊看起來像表格
                const offset = (row % 2) * (stepX / 2);
                const x = Math.min(width - 4, (col + 0.5) * stepX + offset);
                const y = (row + 0.5) * stepY;
                drawMixedText(page, config.text, x, y, config.size, center, fonts.asciiFont,
                    wmColor, { opacity: config.opacity, rotate: fonts.degrees(config.angle), centerX: true });
            }
        }
    }

    function resetWatermarkSettings() {
        document.getElementById('watermarkText').value = '機密';
        document.getElementById('watermarkLayout').value = 'center';
        document.getElementById('watermarkColor').value = 'red';
        document.getElementById('watermarkSize').value = 40;
        document.getElementById('watermarkOpacity').value = 18;
        document.getElementById('watermarkAngle').value = 45;
        document.getElementById('watermarkRange').value = '';
        showNotification('✅ 浮水印設定已重設', 'success');
    }

    // ------------------------------------------------------
    // 復原／重做
    // ------------------------------------------------------

    // 只複製「會變動的結構」：檔案本體與縮圖 data URL 都共用參考，
    // 所以一份快照的額外記憶體約等於頁數 × 幾十 bytes。
    function makeSnapshot() {
        return {
            pdfFiles: pdfFiles.map(file => (file ? {
                ...file,
                pages: file.pages.map(p => ({ ...p })),
            } : file)),
            selectedPages: selectedPages.map(p => ({ ...p })),
            sourceOutlines: new Map(sourceOutlines),
        };
    }

    function applySnapshot(snapshot) {
        pdfFiles = snapshot.pdfFiles.map(file => (file ? { ...file, pages: file.pages.map(p => ({ ...p })) } : file));
        selectedPages = snapshot.selectedPages.map(p => ({ ...p }));
        sourceOutlines = new Map(snapshot.sourceOutlines);
    }

    function updateHistoryButtons() {
        const undoBtn = document.getElementById('undoBtn');
        const redoBtn = document.getElementById('redoBtn');
        if (undoBtn) {
            undoBtn.disabled = undoStack.length === 0;
            undoBtn.textContent = `↶ 復原${undoStack.length ? ` (${undoStack.length})` : ''}`;
        }
        if (redoBtn) {
            redoBtn.disabled = redoStack.length === 0;
            redoBtn.textContent = `↷ 重做${redoStack.length ? ` (${redoStack.length})` : ''}`;
        }
    }

    // 包裝一次「編輯」：先記下變更前的狀態，跑完後補上變更後的狀態，
    // 然後重繪與排程保存。一次呼叫就是一個 undo 步驟，前後的配對不可能漏掉。
    // label 相同且在 HISTORY_MERGE_MS 內會合併成一步（例如拖曳排序的連續事件）。
    function applyEdit(label, fn) {
        const collecting = historyDepth === 0 && !isRestoringSession;
        if (collecting) {
            const now = Date.now();
            const canMerge = label && label === lastHistoryLabel && (now - lastHistoryAt) < HISTORY_MERGE_MS;
            if (!canMerge) {
                undoStack.push({ snapshot: makeSnapshot(), label, after: null });
                if (undoStack.length > HISTORY_LIMIT) undoStack.shift();
                redoStack = [];
            }
            lastHistoryLabel = label;
            lastHistoryAt = now;
        }
        historyDepth++;
        try {
            fn();
        } finally {
            historyDepth--;
        }
        if (collecting) {
            const top = undoStack[undoStack.length - 1];
            if (top && !top.after) top.after = makeSnapshot();
            lastHistoryLabel = null;
            lastHistoryAt = 0;
        }
        updateFileList();
        renderSourcePages();
        renderSelectedPages();
        updateSelectedCountInfo();
        updateHistoryButtons();
        scheduleSessionSave();
    }

    function restoreSnapshot(snapshot) {
        historyDepth++;
        try {
            applySnapshot(snapshot);
            updateFileList();
            renderSourcePages();
            renderSelectedPages();
            updateSelectedCountInfo();
            updateQuickSelectFileOptions();
        } finally {
            historyDepth--;
        }
        updateHistoryButtons();
        scheduleSessionSave();
    }

    function undo() {
        if (undoStack.length === 0) {
            showNotification('沒有可以復原的動作了', 'info');
            return;
        }
        // undoStack 存的是「動作發生前」的狀態，所以直接還原它；
        // 被還原掉的那一步要進 redoStack，redo 才有正確的出口。
        const entry = undoStack.pop();
        redoStack.push(entry);
        restoreSnapshot(entry.snapshot); // 回到「動作發生前」
        showNotification(`↶ 已復原${entry.label ? `：${entry.label}` : ''}`, 'success');
    }

    function redo() {
        if (redoStack.length === 0) {
            showNotification('沒有可以重做的動作了', 'info');
            return;
        }
        // redoStack 裡存的是「動作發生前」的狀態，要回到動作後得先記下現況。
        // 這裡的做法：把「動作後」的狀態還原回去——為此 redo 需要知道動作後的結果，
        // 所以 undo 時把「動作後的快照」一併保存。
        const entry = redoStack.pop();
        undoStack.push(entry);
        // redo 要回到「動作發生後」，所以用當時記下的 after 快照
        restoreSnapshot(entry.after || entry.snapshot);
        showNotification(`↷ 已重做${entry.label ? `：${entry.label}` : ''}`, 'success');
    }

    function resetHistory() {
        undoStack = [];
        redoStack = [];
        lastHistoryLabel = null;
        lastHistoryAt = 0;
        updateHistoryButtons();
    }

    // ------------------------------------------------------
    // 工作階段保存（IndexedDB）
    // 讓不小心重新整理／關掉分頁時，已載入的檔案與編排還在。
    // ------------------------------------------------------

    function openSessionDb() {
        return new Promise((resolve, reject) => {
            if (typeof indexedDB === 'undefined') { reject(new Error('no indexedDB')); return; }
            const req = indexedDB.open(SESSION_DB, 1);
            req.onupgradeneeded = () => {
                const db = req.result;
                if (!db.objectStoreNames.contains(SESSION_STORE)) db.createObjectStore(SESSION_STORE);
            };
            req.onsuccess = () => resolve(req.result);
            req.onerror = () => reject(req.error);
        });
    }

    function idbRequest(req) {
        return new Promise((resolve, reject) => {
            req.onsuccess = () => resolve(req.result);
            req.onerror = () => reject(req.error);
        });
    }

    async function idbPut(key, value) {
        const db = await openSessionDb();
        try {
            const tx = db.transaction(SESSION_STORE, 'readwrite');
            await idbRequest(tx.objectStore(SESSION_STORE).put(value, key));
        } finally { db.close(); }
    }

    async function idbGet(key) {
        const db = await openSessionDb();
        try {
            const tx = db.transaction(SESSION_STORE, 'readonly');
            return await idbRequest(tx.objectStore(SESSION_STORE).get(key));
        } finally { db.close(); }
    }

    async function idbDelete(key) {
        const db = await openSessionDb();
        try {
            const tx = db.transaction(SESSION_STORE, 'readwrite');
            await idbRequest(tx.objectStore(SESSION_STORE).delete(key));
        } finally { db.close(); }
    }

    // 組出可寫入 IndexedDB 的工作階段（File 可被結構化複製，直接存）
    async function buildSessionPayload() {
        const files = [];
        let totalBytes = 0;
        for (const file of pdfFiles) {
            if (!file || !file.file) continue;
            totalBytes += file.file.size || 0;
            files.push({
                name: file.name,
                bytes: new Uint8Array(await file.file.arrayBuffer()),
                outline: file.outline || [],
            });
        }
        return {
            version: 1,
            savedAt: Date.now(),
            files,
            totalBytes,
            selectedPages: selectedPages.map(p => ({ ...p })),
            // Map 不能直接存，轉成陣列
            sourceOutlines: [...sourceOutlines.entries()].map(([name, map]) => [name, [...map.entries()]]),
            view: { viewMode, thumbnailSize, targetViewMode, targetThumbnailSize },
            tocSettings: {
                addToc: addTocCheckbox.checked,
                addBookmarks: !!(document.getElementById('addBookmarksCheckbox') || {}).checked,
                enableLayout: !!(document.getElementById('enableLayoutCheckbox') || {}).checked,
                layoutFit: document.getElementById('layoutFitSelect').value,
                layoutMargin: document.getElementById('layoutMarginInput').value,
                uniformSize: document.getElementById('uniformSizeSelect').value,
                addMarks: !!(document.getElementById('addMarksCheckbox') || {}).checked,
                headerFormat: document.getElementById('headerFormat').value,
                headerPosition: document.getElementById('headerPosition').value,
                footerFormat: document.getElementById('footerFormat').value,
                footerPosition: document.getElementById('footerPosition').value,
                pageNumberFormat: document.getElementById('pageNumberFormat').value,
                pageNumberPosition: document.getElementById('pageNumberPosition').value,
                pageNumberMargin: document.getElementById('pageNumberMargin').value,
                pageNumberSize: document.getElementById('pageNumberSize').value,
                addWatermark: !!(document.getElementById('addWatermarkCheckbox') || {}).checked,
                watermarkText: document.getElementById('watermarkText').value,
                watermarkLayout: document.getElementById('watermarkLayout').value,
                watermarkColor: document.getElementById('watermarkColor').value,
                watermarkSize: document.getElementById('watermarkSize').value,
                watermarkOpacity: document.getElementById('watermarkOpacity').value,
                watermarkAngle: document.getElementById('watermarkAngle').value,
                watermarkRange: document.getElementById('watermarkRange').value,
            },
        };
    }

    // 清掉工作階段：不只要刪資料，還要停掉自動保存，
    // 否則已經排程的 debounce 會在刪除之後又把狀態寫回來（實測就是這樣蓋回去的）。
    async function clearSession() {
        // 不只要刪資料，還要停掉自動保存：已經排程的 debounce、以及正在飛的寫入，
        // 只要晚一步落地就會把剛刪掉的工作階段又寫回來（實測就是這樣蓋回去的）。
        sessionEnabled = false;
        sessionDirty = false;
        if (sessionSaveTimer) { clearTimeout(sessionSaveTimer); sessionSaveTimer = null; }
        sessionSavePending = false;
        if (sessionSavePromise) { try { await sessionSavePromise; } catch (e) {} }
        await idbDelete(SESSION_KEY).catch(() => {});
        // 刪除落地後才恢復自動保存，之後的新編輯照常保存
        sessionEnabled = true;
    }

    // 立即寫入（測試與 beforeunload 用）。成功回傳 true。
    async function flushSessionSave() {
        if (!sessionEnabled || pdfFiles.length === 0) return false;
        try {
            const payload = await buildSessionPayload();
            if (payload.totalBytes > SESSION_MAX_DOC_BYTES) {
                sessionEnabled = false; // 太大，之後不再嘗試
                showNotification('⚠️ 檔案較大，已停止自動保存工作階段（不影響操作）', 'info');
                return false;
            }
            await idbPut(SESSION_KEY, payload);
            sessionDirty = false;
            return true;
        } catch (error) {
            console.error('保存工作階段失敗：', error);
            sessionEnabled = false;
            return false;
        }
    }

    function scheduleSessionSave() {
        if (!sessionEnabled || isRestoringSession) return;
        sessionDirty = true;
        if (sessionSaveTimer) clearTimeout(sessionSaveTimer);
        sessionSaveTimer = setTimeout(() => {
            sessionSaveTimer = null;
            if (sessionSaveInFlight) { sessionSavePending = true; return; }
            sessionSaveInFlight = true;
            sessionSavePromise = flushSessionSave().finally(() => {
                sessionSaveInFlight = false;
                sessionSavePromise = null;
                if (sessionSavePending) { sessionSavePending = false; scheduleSessionSave(); }
            });
        }, SESSION_SAVE_DEBOUNCE_MS);
    }

    // 啟動時詢問是否還原上次的工作階段
    async function maybeRestoreSession() {
        let payload = null;
        try {
            payload = await idbGet(SESSION_KEY);
        } catch (error) {
            return; // 沒有 IndexedDB 或讀不到就當作沒有工作階段
        }
        if (!payload || !Array.isArray(payload.files) || payload.files.length === 0) return;

        const when = payload.savedAt ? new Date(payload.savedAt).toLocaleString() : '上次';
        const names = payload.files.map(f => f.name).join('、');
        const ok = await askConfirm(`偵測到上次的工作階段（${names}，${when}）。要還原嗎？`);
        if (!ok) { await clearSession(); return; }

        const fileList = payload.files.map(f => new File([f.bytes], f.name, { type: 'application/pdf' }));
        isRestoringSession = true;
        progress.textContent = '⏳ 正在還原上次的工作階段...';
        progress.classList.add('active');
        try {
            // 用既有的載入流程重建縮圖，再套回編排
            await handleFiles(fileList);
            // 有檔案沒載回來時 fileIndex 會錯位，套用編排只會對到錯的頁
            if (pdfFiles.length !== payload.files.length) throw new Error('部分檔案無法重新載入');
            payload.files.forEach((f, index) => {
                if (pdfFiles[index]) pdfFiles[index].outline = f.outline || [];
            });
            selectedPages = (payload.selectedPages || []).map(p => ({ ...p }));
            sourceOutlines = new Map((payload.sourceOutlines || []).map(([name, entries]) => [name, new Map(entries)]));
            if (payload.view) {
                viewMode = payload.view.viewMode || viewMode;
                thumbnailSize = payload.view.thumbnailSize || thumbnailSize;
                targetViewMode = payload.view.targetViewMode || targetViewMode;
                targetThumbnailSize = payload.view.targetThumbnailSize || targetThumbnailSize;
            }
            if (payload.tocSettings) {
                addTocCheckbox.checked = !!payload.tocSettings.addToc;
                tocSettingsPanel.style.display = addTocCheckbox.checked ? 'block' : 'none';
                const bmBox = document.getElementById('addBookmarksCheckbox');
                if (bmBox) bmBox.checked = !!payload.tocSettings.addBookmarks;
                const marksBox = document.getElementById('addMarksCheckbox');
                if (marksBox) {
                    // 舊版工作階段存的是 addPageNumbers，沒有 addMarks 時沿用它的值
                    const legacy = payload.tocSettings.addPageNumbers;
                    marksBox.checked = payload.tocSettings.addMarks !== undefined
                        ? !!payload.tocSettings.addMarks : !!legacy;
                    marksSettingsPanel.style.display = marksBox.checked ? 'block' : 'none';
                }
                const wmBox = document.getElementById('addWatermarkCheckbox');
                if (wmBox) {
                    wmBox.checked = !!payload.tocSettings.addWatermark;
                    watermarkSettingsPanel.style.display = wmBox.checked ? 'block' : 'none';
                }
                const setVal = (id, value) => {
                    if (value === undefined || value === null) return;
                    const el = document.getElementById(id);
                    if (el) el.value = value;
                };
                const layoutBox = document.getElementById('enableLayoutCheckbox');
                if (layoutBox) {
                    layoutBox.checked = !!payload.tocSettings.enableLayout;
                    layoutSettingsPanel.style.display = layoutBox.checked ? 'block' : 'none';
                }
                setVal('layoutFitSelect', payload.tocSettings.layoutFit);
                setVal('layoutMarginInput', payload.tocSettings.layoutMargin);
                setVal('uniformSizeSelect', payload.tocSettings.uniformSize);
                setVal('headerFormat', payload.tocSettings.headerFormat);
                setVal('headerPosition', payload.tocSettings.headerPosition);
                setVal('footerFormat', payload.tocSettings.footerFormat);
                setVal('footerPosition', payload.tocSettings.footerPosition);
                setVal('pageNumberFormat', payload.tocSettings.pageNumberFormat);
                setVal('pageNumberPosition', payload.tocSettings.pageNumberPosition);
                setVal('pageNumberMargin', payload.tocSettings.pageNumberMargin);
                setVal('pageNumberSize', payload.tocSettings.pageNumberSize);
                setVal('watermarkText', payload.tocSettings.watermarkText);
                setVal('watermarkLayout', payload.tocSettings.watermarkLayout);
                setVal('watermarkColor', payload.tocSettings.watermarkColor);
                setVal('watermarkSize', payload.tocSettings.watermarkSize);
                setVal('watermarkOpacity', payload.tocSettings.watermarkOpacity);
                setVal('watermarkAngle', payload.tocSettings.watermarkAngle);
                setVal('watermarkRange', payload.tocSettings.watermarkRange);
            }
            setViewMode(viewMode);
            setThumbnailSize(thumbnailSize);
            setTargetViewMode(targetViewMode);
            setTargetThumbnailSize(targetThumbnailSize);
            renderSelectedPages();
            updateSelectedCountInfo();
            resetHistory(); // 還原後不讓使用者 undo 回上一個工作階段
            progress.textContent = `✅ 已還原 ${pdfFiles.length} 個檔案、${selectedPages.length} 個成品項目`;
            progress.classList.add('success');
            setTimeout(() => progress.classList.remove('active', 'success'), 3000);
        } catch (error) {
            console.error('還原工作階段失敗：', error);
            showNotification('⚠️ 還原工作階段失敗，已略過。', 'error');
            progress.classList.remove('active');
        } finally {
            isRestoringSession = false;
        }
    }

    // ------------------------------------------------------
    // 書籤大綱（Outlines）
    // ------------------------------------------------------

    // 把書籤的目的地解析成頁碼索引。dest 可能是名稱字串或陣列，
    // 用文件自己的解析器處理才不會漏掉 /Names、/Dests 這些間接指向。
    async function resolveOutlinePageIndex(pdf, dest) {
        if (dest === undefined || dest === null) return null;
        try {
            const resolved = typeof dest === 'string' ? await pdf.getDestination(dest) : dest;
            if (!Array.isArray(resolved) || resolved.length === 0) return null;
            const index = await pdf.getPageIndex(resolved[0]);
            return Number.isInteger(index) ? index : null;
        } catch (e) {
            return null;
        }
    }

    // 讀出整份文件的書籤，攤平成 [{title, pageIndex, level}]（依文件順序，level 從 0 起算）。
    // 沒有書籤、或讀取失敗都回傳空陣列，不能讓它影響檔案載入。
    async function extractOutline(pdf) {
        try {
            const raw = await pdf.getOutline();
            if (!Array.isArray(raw) || raw.length === 0) return [];
            const flat = [];
            const walk = async (items, level) => {
                for (const item of items || []) {
                    const title = (item && item.title ? String(item.title) : '').trim();
                    const pageIndex = await resolveOutlinePageIndex(pdf, item && item.dest);
                    // 對不到頁的書籤先略過：等於用不到的錨點，留著只會讓後續對位更亂
                    if (title && pageIndex !== null) flat.push({ title, pageIndex, level });
                    if (item && item.items) await walk(item.items, level + 1);
                }
            };
            await walk(raw, 0);
            return flat;
        } catch (error) {
            console.error('讀取書籤大綱失敗（不影響載入）：', error);
            return [];
        }
    }

    // 檔名 -> { 頁碼索引 -> {title, level} }，供預填目錄使用
    function buildOutlineLookup(flat) {
        const byPage = new Map();
        for (const entry of flat) byPage.set(entry.pageIndex, { title: entry.title, level: entry.level });
        return byPage;
    }

    function getOutlineInfoForPage(fileName, pageIndex) {
        const byPage = sourceOutlines.get(fileName);
        return (byPage && byPage.get(pageIndex)) || null;
    }

    // 抽取頁面全文（座標、字級）與標題，一次 getTextContent 完成。
    // items 的 y 為「頂部原點」座標（越小越靠頁頂），供行距重排使用；x 為左側原點。
    async function extractPageInfo(page, pageNum) {
        let items = [];
        try {
            const textContent = await page.getTextContent();
            if (textContent && textContent.items) {
                const viewport = page.getViewport({ scale: 1 });
                const H = viewport.height;
                items = textContent.items
                    .map(item => ({
                        s: item.str ? item.str.trim() : '',
                        x: item.transform ? item.transform[4] : 0,
                        y: item.transform ? H - item.transform[5] : 0,
                        w: item.width || 0,
                        h: item.height || 0,
                    }))
                    .filter(it => it.s.length > 0);
            }
        } catch (error) {
            console.error(`Error extracting text from page ${pageNum}:`, error);
        }
        return { title: buildTitleFromItems(items, pageNum), items: items };
    }

    function buildTitleFromItems(items, pageNum) {        if (items.length === 0) return `Page ${pageNum}`;

        // 依位置排序：y 小（靠頁頂）在前，同行依 x 排序
        const sorted = [...items].sort((a, b) => a.y - b.y || a.x - b.x);
        const lines = [];
        let currentLine = [sorted[0]];
        for (let i = 1; i < sorted.length; i++) {
            if (Math.abs(sorted[i].y - currentLine[0].y) < 5) {
                currentLine.push(sorted[i]);
            } else {
                lines.push(currentLine.sort((a, b) => a.x - b.x));
                currentLine = [sorted[i]];
            }
        }
        lines.push(currentLine.sort((a, b) => a.x - b.x));

        let title = `Page ${pageNum}`;
        if (lines.length > 0 && lines[0].length > 0) {
            let titleLineText = lines[0].map(item => item.s).join(' ');
            if (lines.length > 1 && lines[1].length > 0) {
                const firstLineY = lines[0][0].y;
                const firstLineHeight = lines[0][0].h;
                const secondLineY = lines[1][0].y;
                if (Math.abs(firstLineY - secondLineY) < firstLineHeight * 1.8) {
                    titleLineText += ' ' + lines[1].map(item => item.s).join(' ');
                }
            }

            let cleanedTitle = titleLineText;
            // --- Title cleaning logic ---
            if (!/^\d+\s*年度/.test(cleanedTitle.trim())) {
                cleanedTitle = cleanedTitle.replace(/^[\d\s.\-•]+\s*/, '');
            }
            const stopChars = ['一、', '二、', '（一）', '附註', '說明：', '中華民國'];
            for (const char of stopChars) {
                const pos = cleanedTitle.indexOf(char);
                if (pos !== -1) cleanedTitle = cleanedTitle.substring(0, pos).trim();
            }
            const specialKeywords = ["說明", "表", "情形"];
            let earliestIndex = -1; let keywordLength = 0;
            for (const keyword of specialKeywords) {
                const currentIndex = cleanedTitle.indexOf(keyword);
                if (currentIndex !== -1) {
                    if (earliestIndex === -1 || currentIndex < earliestIndex) {
                        earliestIndex = currentIndex; keywordLength = keyword.length;
                    }
                }
            }
            if (earliestIndex !== -1) {
                cleanedTitle = cleanedTitle.substring(0, earliestIndex + keywordLength);
            }
            cleanedTitle = cleanedTitle.replace(/\s+/g, '');
            if (cleanedTitle.length > 70) {
                cleanedTitle = cleanedTitle.substring(0, 70) + '...';
            }
            // --- End Title cleaning logic ---

            if (cleanedTitle) title = cleanedTitle;
        }
        return title;
    }

    // 這個小節標題後面還有內容頁嗎？（沒有內容頁的小節在產出的目錄上不會出現）
    function isMeaningfulDivider(items, index) {
        const item = items[index];
        if (!item || item.type !== 'divider') return false;
        for (let i = index + 1; i < items.length; i++) {
            if (!items[i]) continue;
            return items[i].type !== 'divider';
        }
        return false;
    }

    // 移除「後面沒有內容頁」的小節標題：來源頁被刪掉後，小節會變成空章節。
    function pruneOrphanDividers(items) {
        return items.filter((item, index) => {
            if (!item || item.type !== 'divider') return true;
            return isMeaningfulDivider(items, index);
        });
    }

    function updateFileList() {
        fileList.innerHTML = pdfFiles.map((file, index) => `
            <li class="file-list-item">
                <span>${esc(file.name)}</span>
                <button class="btn btn-danger" onclick="removeFile(${index})">✕</button>
            </li>
        `).join('');
        updateQuickSelectFileOptions();
    }

    async function removeFile(index) {
        const file = pdfFiles[index];
        // 若該檔頁面已在右側成品中，提醒使用者
        const usedByTarget = selectedPages.some(p => p.type !== 'divider' && p.fileIndex === index);
        if (usedByTarget && !(await askConfirm(`「${file ? file.name : '此檔案'}」有頁面在右側成品中，移除後會一併從成品刪除。確定移除嗎？`))) {
            return;
        }
        applyEdit('移除來源檔案', () => {
            if (file) sourceOutlines.delete(file.name);
            pdfFiles.splice(index, 1);
            selectedPages = selectedPages.filter(p => p.fileIndex !== index).map(p => {
                if (p.fileIndex > index) p.fileIndex--;
                return p;
            });
            selectedPages = pruneOrphanDividers(selectedPages);
        });
    }

    function clearAllFiles() {
        if (pdfFiles.length === 0) return;
        const clearBtn = document.getElementById('clearFilesBtn');
        if (!clearFilesConfirmMode) {
            clearFilesConfirmMode = true;
            clearBtn.classList.add('confirm-mode');
            clearBtn.innerHTML = '🗑️ 確定清除所有檔案？';
            setTimeout(() => {
                clearFilesConfirmMode = false;
                clearBtn.classList.remove('confirm-mode');
                clearBtn.innerHTML = '🗑️ 清除所有檔案';
            }, 3000);
            return;
        }
        clearFilesConfirmMode = false;
        clearBtn.classList.remove('confirm-mode');
        clearBtn.innerHTML = '🗑️ 清除所有檔案';
        fileInput.value = '';
        // 釋放預覽/生成的暫存資源
        finalPdfBytes = null;
        if (previewModal.open) previewModal.close();
        resetHistory(); // 全部清掉之後沒有東西可以復原了
        clearSession();
        applyEdit('清除所有檔案', () => {
            pdfFiles = [];
            selectedPages = [];
            sourceOutlines.clear();
            lastSourceClickGlobalIndex = null;
        });
    }

    // ======================================================
    // === 邏輯區塊：左側來源面板 (Source Panel)
    // ======================================================

    function setViewMode(mode) {
        viewMode = mode;
        document.getElementById('gridViewBtn').classList.toggle('active', mode === 'grid');
        document.getElementById('listViewBtn').classList.toggle('active', mode === 'list');
        renderSourcePages();
    }

    function setThumbnailSize(size) {
        thumbnailSize = size;
        sourcePanel.classList.remove('size-small', 'size-medium', 'size-large', 'size-xlarge');
        sourcePanel.classList.add(`size-${size}`);
        
        document.querySelectorAll('#size-toggle button').forEach(btn => {
            btn.classList.toggle('active', btn.dataset.size === size);
        });
    }

    function renderSourcePages() {
        if (sourceObserver) { sourceObserver.disconnect(); sourceObserver = null; }
        sourceRenderToken++;

        if (pdfFiles.length === 0) {
            sourcePages.innerHTML = '<div class="empty-message"><span class="empty-icon">📂</span>尚未載入任何 PDF 檔案<div class="empty-hint">將 PDF 拖曳到視窗任意位置<br>或點擊左側「選擇檔案」按鈕</div></div>';
            const selectAllSource = document.getElementById('selectAllSource');
            if (selectAllSource) selectAllSource.checked = false; // 避免全選框殘留勾選態
            return;
        }
        // 先建立「骨架」：只放縮圖佔位，捲到可見範圍才由 IntersectionObserver 塞入 <img>。
        // 之前是一次組出所有頁面的 <img src="data:..."> 大字串，500 頁的文件會產生近 500 個
        // 數十 KB 的字串與屬性，主執行緒會被字串串接卡住好幾秒。
        sourcePages.innerHTML = pdfFiles.map((file, fileIndex) => {
            if (!file) return '';
            const itemType = viewMode === 'grid' ? 'grid' : 'list';
            const pagesHtml = file.pages
                .map((page, pageIndex) => renderPageItem(fileIndex, pageIndex, itemType))
                .join('');
            const listClass = viewMode === 'grid' ? 'pages-grid' : 'pages-list';
            return `<div class="pdf-file"><div class="pdf-file-header"><div class="pdf-file-name">${esc(file.name || 'Unknown File')}</div></div><div class="${listClass}">${pagesHtml}</div></div>`;
        }).join('');

        setupSourceLazyRender();
    }

    // 骨架：data-page-key 與勾選框先出來（互動不受影響），縮圖之後才補。
    function renderPageItem(fileIndex, pageIndex, type) {
        if (!pdfFiles[fileIndex] || !pdfFiles[fileIndex].pages[pageIndex]) return '';
        const page = pdfFiles[fileIndex].pages[pageIndex];

        const checkedAttr = page.isChecked ? 'checked' : '';
        const checkedClass = page.isChecked ? 'checked' : '';
        const currentRotation = page.sourceRotation || 0;
        const rotationStyle = `transform: rotate(${currentRotation}deg); transition: transform 0.3s;`;

        const clickAction = `onclick="toggleSourceCheck(${fileIndex}, ${pageIndex}, event)"`;
        const checkboxAction = `onclick="event.stopPropagation(); toggleSourceCheck(${fileIndex}, ${pageIndex}, event)"`;
        const key = `${fileIndex}_${pageIndex}`;

        if (type === 'grid') {
            return `
                <div class="page-item ${checkedClass}" data-page-key="${key}" ${clickAction}>
                    <input type="checkbox" class="page-checkbox" ${checkedAttr} ${checkboxAction}>
                    <div class="page-thumb" data-pending="1">
                        <div class="thumb-placeholder"></div>
                    </div>
                    <div class="page-number">第 ${page.pageNum} 頁</div> 
                </div>`;
        }
        const title = esc(page.firstLine || `Page ${page.pageNum}`);
        return `
                <div class="page-list-item ${checkedClass}" data-page-key="${key}" ${clickAction} title="${title}">
                    <input type="checkbox" class="page-checkbox" ${checkedAttr} ${checkboxAction}>
                    <div class="list-thumb-wrapper" data-pending="1">
                        <div class="thumb-placeholder"></div>
                    </div>
                    <div class="page-list-text">${title}</div>
                    <div class="page-list-number">第 ${page.pageNum} 頁</div>
                </div>`;
    }

    // 把單一骨架換成真正的縮圖（可重複呼叫，已渲染過就自動跳過）
    // 傳進來的可能是骨架本身（[data-pending="1"]）或它所屬的頁面項目。
    function fillSourceThumb(target) {
        const itemEl = target && target.closest ? (target.closest('.page-item, .page-list-item') || target) : null;
        if (!itemEl || !itemEl.dataset) return;
        const wrapper = itemEl.querySelector('[data-pending="1"]');
        if (!wrapper) return; // 已經渲染過
        const [fileIndexStr, pageIndexStr] = (itemEl.dataset.pageKey || '').split('_');
        const fileIndex = Number(fileIndexStr);
        const pageIndex = Number(pageIndexStr);
        const page = pdfFiles[fileIndex] && pdfFiles[fileIndex].pages[pageIndex];
        if (!page) return;

        const rotationStyle = `transform: rotate(${(page.sourceRotation || 0) % 360}deg); transition: transform 0.3s;`;
        // 不要在這裡寫 width:100%：清單模式的縮圖框只有 34×44，會被這條規則覆蓋成扁細的一條。
        // 尺寸交給 CSS（grid 用 width:100%，list 用 max-width/max-height + object-fit: contain）。
        wrapper.innerHTML = page.thumb
            ? `<img class="page-thumb-img" src="${page.thumb}" alt="第 ${page.pageNum} 頁縮圖" decoding="async" style="${rotationStyle}">`
            : `<div class="thumb-fallback" style="${rotationStyle}">無法預覽</div>`;
        delete wrapper.dataset.pending;
    }

    function setupSourceLazyRender() {
        const skeletons = Array.from(sourcePages.querySelectorAll('[data-pending="1"]'));
        if (skeletons.length === 0) return;
        const token = sourceRenderToken;

        if (typeof IntersectionObserver === 'undefined') {
            skeletons.forEach(fillSourceThumb); // 老瀏覽器：直接全部渲染
            return;
        }

        // 先把「目前可視範圍內」的縮圖同步補上。
        // IntersectionObserver 的回呼只在下一次繪製幀才跑，若完全依賴它，剛載入完那一瞬間
        // 畫面會是空的（截圖與自動化測試尤其明顯）。getBoundingClientRect() 會強制 layout，
        // 之後就能用座標自行判斷可見性。
        const rootRect = sourcePages.getBoundingClientRect();
        const viewTop = rootRect.top - 400;
        const viewBottom = rootRect.bottom + 400;
        const rest = [];
        for (const el of skeletons) {
            const rect = el.getBoundingClientRect();
            if (rect.bottom >= viewTop && rect.top <= viewBottom) {
                fillSourceThumb(el);
            } else {
                rest.push(el);
            }
        }
        if (rest.length === 0) return;

        sourceObserver = new IntersectionObserver((entries) => {
            if (token !== sourceRenderToken) { sourceObserver.disconnect(); return; }
            for (const entry of entries) {
                if (!entry.isIntersecting) continue;
                fillSourceThumb(entry.target);
                sourceObserver.unobserve(entry.target);
            }
        }, { root: sourcePages, rootMargin: '400px 0px' });
        rest.forEach(el => sourceObserver.observe(el));
    }

    // 核心選取邏輯 (支援 Shift)
    function toggleSourceCheck(fileIndex, pageIndex, event) {
        if (!pdfFiles[fileIndex] || !pdfFiles[fileIndex].pages[pageIndex]) return;
        
        const currentGlobalIndex = getGlobalPageIndex(fileIndex, pageIndex);
        const targetPage = pdfFiles[fileIndex].pages[pageIndex];

        // 判斷是否按住了 Shift 鍵，且之前有點擊過有效的位置
        if (event && event.shiftKey && lastSourceClickGlobalIndex !== null) {
            
            // 1. 先改變「當前點擊頁面」的狀態
            targetPage.isChecked = !targetPage.isChecked;
            const targetState = targetPage.isChecked; 

            // 2. 計算範圍
            const start = Math.min(lastSourceClickGlobalIndex, currentGlobalIndex);
            const end = Math.max(lastSourceClickGlobalIndex, currentGlobalIndex);

            // 3. 迴圈設定狀態
            for (let i = start; i <= end; i++) {
                const pos = getPageByGlobalIndex(i);
                if (pos) {
                    pdfFiles[pos.fileIndex].pages[pos.pageIndex].isChecked = targetState;
                }
            }
            lastSourceClickGlobalIndex = currentGlobalIndex;
            syncSourceCheckDom();
        } else {
            // 一般單點：就地更新該項目，避免整面重繪造成閃爍
            targetPage.isChecked = !targetPage.isChecked;
            lastSourceClickGlobalIndex = currentGlobalIndex;
            const thumbEl = document.querySelector(`[data-page-key="${fileIndex}_${pageIndex}"]`);
            const itemEl = thumbEl && thumbEl.closest('.page-item, .page-list-item');
            if (itemEl) {
                itemEl.classList.toggle('checked', targetPage.isChecked);
                const cb = itemEl.querySelector('.page-checkbox');
                if (cb) cb.checked = targetPage.isChecked;
            } else {
                renderSourcePages();
            }
        }
        updateSelectedCountInfo();
    }

    function toggleSelectAllSource(checkbox) {
        const isChecked = checkbox.checked;
        pdfFiles.forEach(file => {
            file.pages.forEach(page => {
                page.isChecked = isChecked;
            });
        });
        syncSourceCheckDom();
        updateSelectedCountInfo();
    }

    // --- 批次操作 (Source) ---

    function batchAddToTarget() {
        if (!pdfFiles.some(file => file && file.pages.some(p => p.isChecked))) {
            showNotification('⚠️ 請先勾選要加入的頁面', 'info');
            return;
        }
        let addedCount = 0;
        applyEdit('加入右側', () => {
        pdfFiles.forEach((file, fIndex) => {
            file.pages.forEach((page, pIndex) => {
                if (page.isChecked) {
                    selectedPages.push({ 
                        type: 'page', 
                        fileIndex: fIndex, 
                        pageNum: page.pageNum, 
                        fileName: file.name, 
                        thumb: page.thumb, 
                        firstLine: page.firstLine,
                        // 來源書籤（若該頁有被書籤指到）：預填目錄與寫入成品書籤都用得到
                        sourceTitle: page.sourceTitle || null,
                        level: page.sourceLevel || 0,
                        contentBox: page.contentBox || null,
                        rotation: page.sourceRotation || 0 
                    });
                    addedCount++;
                }
            });
        });

        });
        showNotification(`✅ 已加入 ${addedCount} 個頁面到右側`, 'success');
        const container = document.getElementById('selectedPages');
        container.scrollTop = container.scrollHeight;
    }

    async function batchDeleteFromSource() {
        let deletedCount = 0;
        let hasSelection = false;
        pdfFiles.forEach(f => f.pages.forEach(p => { if(p.isChecked) hasSelection = true; }));

        if (!hasSelection) {
            showNotification('⚠️ 請先勾選要刪除的頁面', 'info');
            return;
        }

        if (!await askConfirm("確定要從來源列表中刪除選取的頁面嗎？")) return;

        applyEdit('刪除來源頁面', () => {
            // 必須在 applyEdit 裡才改資料：快照在 fn 之前拍，先改就無法復原
            const newPdfFiles = [];
            const indexMap = new Map(); // 舊 fileIndex -> 新 fileIndex（整檔刪除時右側索引須重排）
            pdfFiles.forEach((file, oldIndex) => {
                const remainingPages = file.pages.filter(p => {
                    if (p.isChecked) {
                        deletedCount++;
                        return false;
                    }
                    return true;
                });
                if (remainingPages.length > 0) {
                    file.pages = remainingPages;
                    indexMap.set(oldIndex, newPdfFiles.length);
                    newPdfFiles.push(file);
                }
            });
            pdfFiles = newPdfFiles;
            selectedPages = selectedPages.filter(p => p.type === 'divider' || p.type === 'blank' || indexMap.has(p.fileIndex));
            selectedPages.forEach(p => { if (p.type === 'page') p.fileIndex = indexMap.get(p.fileIndex); });
            // 來源被刪光的小節標題會變成孤兒（後面沒有內容頁），留著只會讓目錄多出空章節。
            selectedPages = pruneOrphanDividers(selectedPages);
            document.getElementById('selectAllSource').checked = false;
        });
        showNotification(`🗑️ 已刪除 ${deletedCount} 個頁面`, 'success');
    }

    function batchRotateSource(deg) {
        if (!pdfFiles.some(file => file.pages.some(p => p.isChecked))) {
            showNotification('⚠️ 請先勾選要旋轉的頁面 (左側來源)', 'info');
            return;
        }
        let rotatedCount = 0;
        applyEdit('旋轉來源頁面', () => {
            pdfFiles.forEach(file => {
                file.pages.forEach(page => {
                    if (!page.isChecked) return;
                    page.sourceRotation = ((page.sourceRotation || 0) + deg + 360) % 360;
                    rotatedCount++;
                });
            });
        });
        applySourceRotationDom();
        showNotification(`↻ 已旋轉 ${rotatedCount} 個頁面`, 'success');
    }

    // --- 快速選取 (Source) ---

    function updateQuickSelectFileOptions() {
        const qsFileSelect = document.getElementById('qsFileSelect');
        if (!qsFileSelect) return;

        qsFileSelect.innerHTML = '';
        if (pdfFiles.length === 0) {
            const option = document.createElement('option');
            option.value = "-1"; option.text = "-- 請先載入檔案 --";
            qsFileSelect.appendChild(option);
            return;
        }

        const allOption = document.createElement('option');
        allOption.value = "-1"; allOption.text = "📂 所有已載入檔案";
        qsFileSelect.appendChild(allOption);

        pdfFiles.forEach((file, index) => {
            const option = document.createElement('option');
            option.value = index;
            option.text = `📄 ${index + 1}. ${file.name}`;
            qsFileSelect.appendChild(option);
        });
    }

    // 智慧勾選：以「取代」語意運作（等於一次篩選），
    // 只作用在選定的檔案上；沒被選到的檔案維持原狀，避免跨檔誤清。
    async function applyQuickSelection() {
        const fileIndexStr = document.getElementById('qsFileSelect').value;
        const type = document.getElementById('qsTypeSelect').value;
        const targetFileIndex = parseInt(fileIndexStr);

        if (pdfFiles.length === 0) {
            showNotification('請先載入 PDF 檔案', 'error');
            return;
        }

        const targetFiles = targetFileIndex === -1
            ? pdfFiles.map((file, index) => ({ file, index }))
            : (pdfFiles[targetFileIndex] ? [{ file: pdfFiles[targetFileIndex], index: targetFileIndex }] : []);

        if (targetFiles.length === 0) {
            showNotification('沒有符合條件的頁面', 'info');
            return;
        }

        // 先問一次，讓「取代」不會靜默吃掉使用者手動勾的頁面
        const alreadyChecked = targetFiles.reduce((n, { file }) => n + file.pages.filter(p => p.isChecked).length, 0);
        if (alreadyChecked > 0) {
            const scopeName = targetFileIndex === -1 ? '所有檔案' : `「${pdfFiles[targetFileIndex].name}」`;
            // eslint-disable-next-line no-alert
            const ok = await askConfirm(`${scopeName} 目前有 ${alreadyChecked} 頁已勾選，套用智慧勾選會先清除這些勾選。要繼續嗎？`);
            if (!ok) return;
        }

        let matchCount = 0;
        for (const { file } of targetFiles) {
            file.pages.forEach((page, pIndex) => {
                const pageNum = page.pageNum;
                let shouldCheck = false;
                switch (type) {
                    case 'all': shouldCheck = true; break;
                    case 'odd': shouldCheck = (pageNum % 2 !== 0); break;
                    case 'even': shouldCheck = (pageNum % 2 === 0); break;
                    case 'first': shouldCheck = (pIndex === 0); break;
                    case 'last': shouldCheck = (pIndex === file.pages.length - 1); break;
                    case 'blank': shouldCheck = (page.firstLine === `Page ${pageNum}`); break;
                }
                page.isChecked = shouldCheck;
                if (shouldCheck) matchCount++;
            });
        }

        const selectAllSource = document.getElementById('selectAllSource');
        if (selectAllSource) selectAllSource.checked = false;
        syncSourceCheckDom();
        updateSelectedCountInfo();

        if (matchCount > 0) {
            showNotification(`已勾選 ${matchCount} 個頁面`, 'success');
        } else {
            showNotification('沒有符合條件的頁面（原有勾選已清除）', 'info');
        }
    }

    function clearAllSourceChecks() {
        pdfFiles.forEach(file => {
            file.pages.forEach(page => page.isChecked = false);
        });
        document.getElementById('selectAllSource').checked = false;
        syncSourceCheckDom();
        updateSelectedCountInfo();
    }

    // ======================================================
    // === 邏輯區塊：右側成品面板 (Target Panel)
    // ======================================================

    function setTargetViewMode(mode) {
        targetViewMode = mode;
        document.getElementById('targetGridViewBtn').classList.toggle('active', mode === 'grid');
        document.getElementById('targetListViewBtn').classList.toggle('active', mode === 'list');
        renderSelectedPages();
    }

    function setTargetThumbnailSize(size) {
        targetThumbnailSize = size;
        const container = document.getElementById('targetPanel');
        container.classList.remove('size-small', 'size-medium', 'size-large', 'size-xlarge');
        container.classList.add(`size-${size}`);
        
        document.querySelectorAll('#target-size-toggle button').forEach(btn => {
            btn.classList.toggle('active', btn.dataset.size === size);
        });
    }

    function renderSelectedPages() {
        if (selectedPages.length === 0) {
            selectedPagesContainer.innerHTML = '<div class="empty-message"><span class="empty-icon">📋</span>尚未選擇任何頁面<div class="empty-hint">在左側勾選頁面後，點「➕ 加入右側」<br>或使用「⚡ 智慧勾選」快速選取</div></div>';
            // 即使是空訊息，也重置樣式以免跑版
            selectedPagesContainer.style.display = 'flex';
            selectedPagesContainer.style.flexDirection = 'column';
            const selectAllTarget = document.getElementById('selectAllTarget');
            if (selectAllTarget) selectAllTarget.checked = false; // 避免全選框殘留勾選態
            updateTargetSelectedInfo();
            return;
        }

        selectedPagesContainer.className = `selected-pages ${targetViewMode}-view`;

        // 依檢視模式設定容器的 flex 方向
        if (targetViewMode === 'list') {
            selectedPagesContainer.style.display = 'flex';
            selectedPagesContainer.style.flexDirection = 'column'; // 強制由上而下
            selectedPagesContainer.style.flexWrap = 'nowrap';
            selectedPagesContainer.style.alignContent = 'stretch';
        } else {
            // Grid 模式
            selectedPagesContainer.style.display = 'flex';
            selectedPagesContainer.style.flexDirection = 'row';
            selectedPagesContainer.style.flexWrap = 'wrap';
            selectedPagesContainer.style.alignContent = 'flex-start';
        }

        selectedPagesContainer.innerHTML = selectedPages.map((item, index) => {
             if (!item) return '';
             
             // 分隔線
             if (item.type === 'divider') {
                // 分隔線在任何模式下都應該佔滿整行
                // 「後面沒有內容頁」的小節不會出現在目錄上，先在畫面上標示出來。
                const empty = !isMeaningfulDivider(selectedPages, index);
                return `
                    <div class="selected-divider-item${empty ? ' empty-section' : ''}" data-index="${index}"
                         title="${empty ? '這個小節後面沒有內容頁，不會出現在產出的目錄上' : '小節標題'}">
                        <span class="drag-handle">::</span>
                         <div class="selected-divider-title">${esc(item.firstLine || 'New Section')}${empty ? '（空章節，不列入目錄）' : ''}</div>
                        <div class="page-actions">
                            <button class="btn btn-danger" onclick="removeSelectedPage(${index})">✕</button>
                        </div>
                    </div>`;
            }

            // 一般頁面
            const isBlank = item.type === 'blank';
            const title = esc(item.firstLine || (isBlank ? '空白頁' : `Page ${item.pageNum || '?'}`));
            const source = isBlank ? '插入的空白頁' : esc(`${item.fileName || 'Unknown File'} - 第 ${item.pageNum || '?'} 頁`);
            const fallbackText = isBlank ? '空白頁' : '無法預覽';
            const checkedAttr = item.isChecked ? 'checked' : '';
            const checkedClass = item.isChecked ? 'checked' : '';
            const rotationStyle = `transform: rotate(${item.rotation || 0}deg);`;
            const clickAction = `onclick="toggleTargetCheck(${index})"`;

            if (targetViewMode === 'grid') {
                return `
                <div class="selected-page-item grid-item ${checkedClass}" data-index="${index}" ${clickAction}>
                    <input type="checkbox" class="page-checkbox" ${checkedAttr} onclick="event.stopPropagation(); toggleTargetCheck(${index})">
                    <div class="canvas-wrapper">
                        ${item.thumb
                            ? `<img class="page-thumb-img" src="${item.thumb}" alt="第 ${index + 1} 張縮圖" loading="lazy" decoding="async" style="${rotationStyle}">`
                            : `<div class="thumb-fallback" style="${rotationStyle}">${fallbackText}</div>`}
                    </div>
                    <div class="page-info-grid">
                        <div class="page-num-badge">${index + 1}</div>
                        <div class="page-title-grid" title="${title}">${title}</div>
                    </div>
                </div>`;
            } else {
                // List Item 需要佔滿整行寬度
                return `
                <div class="selected-page-item list-item ${checkedClass}" data-index="${index}" ${clickAction}>
                    <span class="drag-handle">::</span>
                    <input type="checkbox" class="page-checkbox" ${checkedAttr} onclick="event.stopPropagation(); toggleTargetCheck(${index})">
                    <div class="list-thumb-wrapper">
                        ${item.thumb
                            ? `<img class="page-thumb-img" src="${item.thumb}" alt="第 ${index + 1} 張縮圖" loading="lazy" decoding="async" style="${rotationStyle}">`
                            : `<div class="thumb-fallback" style="${rotationStyle}">${fallbackText}</div>`}
                    </div>
                    <div class="selected-page-info">
                        <div class="selected-page-title">${index + 1}. ${title}</div>
                        <div class="selected-page-source">${source}</div>
                    </div>
                </div>`;
            }
        }).join('');

        // 縮圖是 data URL，由 <img> 直接呈現
        updateTargetSelectedInfo();
    }

    function toggleTargetCheck(index) {
        if (!selectedPages[index]) return;
        const checked = selectedPages[index].isChecked = !selectedPages[index].isChecked;
        const itemEl = selectedPagesContainer.querySelector(`[data-index="${index}"]`);
        if (itemEl) {
            itemEl.classList.toggle('checked', checked);
            const cb = itemEl.querySelector('.page-checkbox');
            if (cb) cb.checked = checked;
        } else {
            renderSelectedPages();
        }
        updateTargetSelectedInfo();
    }

    function toggleSelectAllTarget(checkbox) {
        const checked = checkbox.checked;
        selectedPages.forEach(p => {
            if (p.type !== 'divider') p.isChecked = checked;
        });
        syncTargetCheckDom();
        updateTargetSelectedInfo();
    }

    // --- 批次操作 (Target) ---

    function batchDeleteFromTarget() {
        if (!selectedPages.some(p => p && p.type !== 'divider' && p.isChecked)) {
            showNotification('請先勾選右側頁面', 'info');
            return;
        }
        const initialLen = selectedPages.length;
        applyEdit('從成品刪除頁面', () => {
            // 小節分隔線沒有 isChecked，不能被當成「未勾選」而意外刪掉；
            // 勾選語意只針對內容頁，分隔線一律保留。
            selectedPages = selectedPages.filter(p => p && (p.type === 'divider' || !p.isChecked));
            document.getElementById('selectAllTarget').checked = false;
        });
        showNotification(`已從右側移除 ${initialLen - selectedPages.length} 頁`, 'success');
    }

    function batchRotateTarget(deg) {
        if (!selectedPages.some(p => p && p.isChecked && p.type !== 'divider')) {
            showNotification('請先勾選右側頁面', 'info');
            return;
        }
        applyEdit('旋轉成品頁面', () => {
            selectedPages.forEach(p => {
                if (p.isChecked && p.type !== 'divider') {
                    const current = p.rotation || 0;
                    p.rotation = (current + deg + 360) % 360;
                }
            });
        });
        applyTargetRotationDom();
    }

    function removeSelectedPage(index) {
        applyEdit('移除單一頁面', () => {
            selectedPages.splice(index, 1);
        });
    }

    // 與左側一致：智慧勾選＝一次篩選（取代現有勾選）。
    // 小節分隔線不佔「第幾張」的序號，所以頁序以內容頁自己的位置計算。
    async function applyTargetQuickSelection() {
        const type = document.getElementById('qsTargetTypeSelect').value;
        const pageItems = selectedPages.filter(p => p && p.type !== 'divider');
        if (pageItems.length === 0) {
            showNotification('右側還沒有任何頁面', 'info');
            return;
        }

        const alreadyChecked = selectedPages.filter(p => p && p.type !== 'divider' && p.isChecked).length;
        if (alreadyChecked > 0) {
            const ok = await askConfirm(`右側目前有 ${alreadyChecked} 頁已勾選，套用智慧勾選會先清除這些勾選。要繼續嗎？`);
            if (!ok) return;
        }

        let count = 0;
        pageItems.forEach((item, pos) => {
            let shouldCheck = false;
            switch (type) {
                case 'all': shouldCheck = true; break;
                case 'odd': shouldCheck = (pos % 2 === 0); break;   // 第 1,3,5... 張
                case 'even': shouldCheck = (pos % 2 === 1); break;  // 第 2,4,6... 張
                case 'first': shouldCheck = (pos === 0); break;
                case 'last': shouldCheck = (pos === pageItems.length - 1); break;
                case 'blank': shouldCheck = !!item.firstLine && item.firstLine.startsWith('Page '); break;
            }
            item.isChecked = shouldCheck;
            if (shouldCheck) count++;
        });

        renderSelectedPages();
        showNotification(count > 0 ? `已勾選右側 ${count} 個頁面` : '沒有符合條件的頁面（原有勾選已清除）', count > 0 ? 'success' : 'info');
    }

    function clearSelectedPages() {
        if (selectedPages.length === 0) return;
        const btn = document.getElementById('clearSelectedBtn');
        if (!clearSelectedConfirmMode) {
            clearSelectedConfirmMode = true;
            btn.classList.add('confirm-mode');
            btn.textContent = '確定清空全部？';
            setTimeout(() => {
                clearSelectedConfirmMode = false;
                btn.classList.remove('confirm-mode');
                btn.textContent = '🗑️ 清空全部';
            }, 3000);
            return;
        }
        clearSelectedConfirmMode = false;
        btn.classList.remove('confirm-mode');
        btn.textContent = '🗑️ 清空全部';
        applyEdit('清空成品', () => {
            selectedPages = [];
        });
    }

    async function addSectionDivider() {
        const title = await askText("請輸入小節標題：");
        if (title && title.trim()) {
            applyEdit('新增小節', () => {
                selectedPages.push({
                    type: 'divider',
                    firstLine: title.trim(),
                    id: Date.now()
                });
            });
        }
    }

    // 空白頁：插在最後一個勾選的頁面之後，沒有勾選就加在最後。
    // firstLine 留空，才不會被當成目錄／書籤項目。
    function insertBlankPage() {
        let at = selectedPages.length;
        for (let i = selectedPages.length - 1; i >= 0; i--) {
            if (selectedPages[i] && selectedPages[i].isChecked) { at = i + 1; break; }
        }
        applyEdit('插入空白頁', () => {
            selectedPages.splice(at, 0, { type: 'blank', firstLine: null, thumb: null, rotation: 0 });
        });
    }

    // 空白頁的尺寸跟前一頁一樣，沒有前一頁就用 A4
    function addBlankPdfPage(doc, prevPage, rotation) {
        const size = prevPage ? [prevPage.getWidth(), prevPage.getHeight()] : [595.28, 841.89];
        const page = doc.addPage(size);
        page.setRotation(PDFLib.degrees(rotation || 0));
        return page;
    }

    // ======================================================
    // === 邏輯區塊：拖曳排序 (Drag & Drop)
    // ======================================================

    // 使用 SortableJS：grid/list 皆準確，支援觸控與動畫。綁在容器上，初始化一次即可
    function setupDragAndDrop() {
        Sortable.create(selectedPagesContainer, {
            animation: 150,
            draggable: '.selected-page-item, .selected-divider-item',
            ghostClass: 'dragging',
            onEnd: (evt) => {
                if (evt.oldIndex === evt.newIndex) return;
                applyEdit('調整頁面順序', () => {
                    const [movedItem] = selectedPages.splice(evt.oldIndex, 1);
                    selectedPages.splice(evt.newIndex, 0, movedItem);
                });
            }
        });
    }

    // ======================================================
    // === 邏輯區塊：目錄編輯 (TOC Editor)
    // ======================================================

    // 縮排 ↔ 階層：行首每 2 個空白算一層（上限 5 層，避免誤貼大段空白爆掉）
    // 目錄編輯器的行首語法：[roman-lower] 之類的標記代表「從這裡開始用這個編號方案」，
    // 只有頁面行會有作用；標記本身不佔行數。
    function schemeFromLine(rawLine) {
        const match = String(rawLine).match(/^\s*\[([a-z-]+)\]\s*/i);
        if (!match) return { scheme: null, rest: rawLine };
        const key = match[1].toLowerCase();
        if (!NUMBERING_SCHEMES[key]) return { scheme: null, rest: rawLine };
        return { scheme: key, rest: String(rawLine).slice(match[0].length) };
    }

    function levelFromIndent(line) {
        const match = line.match(/^[ \t]*/);
        const indent = match ? match[0].replace(/\t/g, '  ').length : 0;
        return Math.min(5, Math.floor(indent / 2));
    }

    function openTocEditor() {
        const pageItems = selectedPages.filter(p => p && p.type !== 'divider');
        if (pageItems.length === 0) {
            showNotification('請先選擇至少一個頁面才能編輯目錄。', 'info');
            return;
        }
        const titles = pageItems
            .map(p => {
                // 有指定編號方案時帶出標記，使用者才看得到目前設定
                const tag = p.scheme ? `[${p.scheme}] ` : '';
                return tag + '  '.repeat(p.level || 0) + (p.firstLine || `Page ${p.pageNum || '?'}`);
            })
            .join('\n');
        tocTextarea.value = titles;
        tocModal.showModal();
    }

    function closeTocEditor() {
        tocModal.close();
    }

    // 用來源 PDF 的書籤大綱預填目錄：這是「不用一行一行手打」的關鍵。
    // 以每頁的原始頁碼去對照來源書籤（書籤是標在頁上的，跟加入順序無關）。
    function prefillTocFromSource() {
        const pageItems = selectedPages.filter(p => p && p.type !== 'divider');
        if (pageItems.length === 0) {
            showNotification('請先選擇至少一個頁面。', 'info');
            return;
        }
        let hitCount = 0;
        const lines = pageItems.map(item => {
            const file = pdfFiles[item.fileIndex];
            const info = getOutlineInfoForPage(file && file.name, item.pageNum - 1);
            if (info) hitCount++;
            // 有來源書籤就用它（含階層），沒有的頁面沿用目前的標題、不縮排
            if (info) return '  '.repeat(info.level) + info.title;
            return item.firstLine || `Page ${item.pageNum || '?'}`;
        });
        tocTextarea.value = lines.join('\n');
        if (hitCount === 0) {
            showNotification('來源 PDF 沒有書籤，已保留目前的標題。', 'info');
        } else {
            showNotification(`✅ 已帶入 ${hitCount}/${pageItems.length} 頁的來源書籤`, 'success');
        }
    }

    function saveToc() {
        const rawLines = tocTextarea.value.split('\n');
        const pageItems = selectedPages.filter(p => p && p.type !== 'divider');
        if (rawLines.length !== pageItems.length) {
            showNotification(`錯誤：目錄行數 (${rawLines.length}) 與選擇的頁數 (${pageItems.length}) 不符。`, 'error');
            return;
        }
        let titleIndex = 0;
        applyEdit('編輯目錄', () => {
            selectedPages.forEach(item => {
                if (item && item.type !== 'divider') {
                    const raw = rawLines[titleIndex] || '';
                    // 行首的 [scheme] 標記決定編號方案（同一小節內沿用，不佔行數）
                    const parsed = schemeFromLine(raw);
                    if (parsed.scheme) item.scheme = parsed.scheme;
                    else delete item.scheme;
                    const title = parsed.rest.replace(/^[ \t]+/, '').trim();
                    item.firstLine = title || `Page ${item.pageNum || '?'}`;
                    item.level = levelFromIndent(parsed.rest);
                    titleIndex++;
                }
            });
        });
        closeTocEditor();
    }

    function resetTocSettings() {
        document.getElementById('tocMainTitleSize').value = 20;
        document.getElementById('tocSectionSize').value = 14;
        document.getElementById('tocItemTitleSize').value = 12;
        document.getElementById('tocPageNumSize').value = 12;
        document.getElementById('tocLineHeight').value = 20;
        showNotification('✅ 已重設為預設值', 'success');
    }

    // ======================================================
    // === 邏輯區塊：PDF 生成與下載 (Generation)
    // ======================================================

    async function downloadGeneratedPDF() {
        if (!finalPdfBytes) {
            showNotification("沒有可下載的 PDF 檔案。", 'error');
            return;
        }

        const blob = new Blob([finalPdfBytes], { type: 'application/pdf' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.style.display = 'none';

        const defaultFileName = '重組後的PDF_' + new Date().toISOString().slice(0, 10) + '.pdf';
        let finalFileName = await askText("請確認檔案名稱：", defaultFileName);

        if (finalFileName === null) {
            URL.revokeObjectURL(url);
            return;
        }
        if (finalFileName.trim() === "") {
            finalFileName = defaultFileName;
        }
        a.download = finalFileName.endsWith('.pdf') ? finalFileName : finalFileName + '.pdf';

        document.body.appendChild(a);
        a.click();
        
        setTimeout(() => {
            try {
                document.body.removeChild(a);
                URL.revokeObjectURL(url); 
            } catch (cleanupError) { console.error("Error during cleanup:", cleanupError); }
        }, 100);
        
        closePreview();
    }

    function closePreview() {
        // 資源清理統一在 previewModal 的 'close' 事件處理
        if (previewModal.open) previewModal.close();
    }

    // 混合字型繪製：ASCII 用 Helvetica（抽取正確），CJK 用思源黑體。
    // 原因：pdf-lib + fontkit 對 CJK 字型的 ASCII 子集化會產生錯誤 glyph 對映
    // （渲染正常，但搜尋/複製會得到亂碼），拆開用標準字型則完全正常。
    function splitMixedRuns(text) {
        const runs = [];
        let cur = null;
        for (const ch of text) {
            const isAscii = ch.codePointAt(0) <= 0x7F;
            if (!cur || cur.ascii !== isAscii) {
                cur = { ascii: isAscii, s: ch };
                runs.push(cur);
            } else {
                cur.s += ch;
            }
        }
        return runs;
    }

    function mixedTextWidth(text, size, cjkFont, asciiFont) {
        let w = 0;
        for (const run of splitMixedRuns(text)) {
            w += (run.ascii ? asciiFont : cjkFont).widthOfTextAtSize(run.s, size);
        }
        return w;
    }

    // 沒有中文字型時的保險：Helvetica 只認 WinAnsi，遇到中文會直接丟錯。
    // 若放任它丟，該行會整條消失（錯誤被上層 catch 吞掉），使用者只看到空目錄；
    // 這裡主動換成 '?' 並在前面統一提一次警告。
    function sanitizeWinAnsi(text) {
        return String(text).replace(/[^\x20-\x7E\u00A0-\u00FF]/g, '?');
    }

    // opts.opacity / opts.rotate 直接轉給 drawText（浮水印用）。
    // opts.centerX：把 x 當成「中心點」，由呼叫端負責算好總寬再逐段回推。
    // 預設（不給 opts）行為與原本完全相同，目錄與頁碼不受影響。
    // 呼叫端注意：沒有中文字型時要傳 cjkFontAvailable:false，中文會被換成 '?'。
    function drawMixedText(page, text, x, y, size, cjkFont, asciiFont, color, opts = {}) {
        const { cjkFontAvailable = true, opacity, rotate, centerX = false } = opts;
        const safeText = cjkFontAvailable ? String(text) : sanitizeWinAnsi(text);
        const runs = splitMixedRuns(safeText);
        const extra = {};
        if (typeof opacity === 'number') extra.opacity = opacity;
        if (rotate) extra.rotate = rotate;

        let cx = x;
        if (centerX) {
            // 先量總寬，再從中心點往左推
            let total = 0;
            for (const run of runs) {
                const font = run.ascii ? asciiFont : cjkFont;
                try { total += font.widthOfTextAtSize(run.s, size); } catch (e) { /* 量不到就當 0 */ }
            }
            cx = x - total / 2;
        }
        for (const run of runs) {
            const font = run.ascii ? asciiFont : cjkFont;
            page.drawText(run.s, { x: cx, y: y, size: size, font: font, color: color, ...extra });
            cx += font.widthOfTextAtSize(run.s, size);
        }
        return cx - x; // 回傳總寬度
    }

    // ------------------------------------------------------
    // 把目錄寫成 PDF 書籤大綱（閱讀器左側的原生導覽）
    // ------------------------------------------------------

    // 中文書籤必須用 UTF-16BE + BOM 的 hex 字串：PDFString 走的是 PDFDocEncoding，
    // 直接塞中文會在閱讀器裡變成亂碼（實測會得到 ", à" 這種字）。
    function pdfTextString(text, PDFString, PDFHexString) {
        const str = String(text == null ? '' : text);
        if (/^[\x00-\x7F]*$/.test(str)) return PDFString.of(str);
        const bytes = [0xFE, 0xFF];
        for (const ch of str) {
            const cp = ch.codePointAt(0);
            if (cp < 0x10000) {
                bytes.push(cp >> 8, cp & 0xFF);
            } else {
                const v = cp - 0x10000;
                const hi = 0xD800 + (v >> 10), lo = 0xDC00 + (v & 0x3FF);
                bytes.push(hi >> 8, hi & 0xFF, lo >> 8, lo & 0xFF);
            }
        }
        return PDFHexString.of(bytes.map(b => b.toString(16).padStart(2, '0')).join(''));
    }

    // 把 [{title, level, page}] 攤平清單組成樹狀大綱節點。
    // 先配置 PDFRef 再互相連結：pdf-lib 的 context.obj() 遇到 PDFRef 會保留參考，
    // 但直接放未註冊的物件會被深拷貝，Next/Prev 的循環參考就會斷掉。
    function buildOutlineTree(doc, entries, PDFString, PDFHexString) {
        const ctx = doc.context;
        // 傳進來的 level 是「目錄頁的縮排層級」，最淺的一項不一定是 0
        // （有小節標題時，內容頁是從 1 起算）。書籤樹必須以最淺的那一項當第 0 層，
        // 否則每一項都會因為「找不到 level-1 的父節點」而全部變成頂層。
        const minLevel = entries.reduce((min, entry, index) => (index === 0 ? (entry.level || 0) : Math.min(min, entry.level || 0)), 0);
        const items = entries.map((entry, index) => {
            const normalized = (entry.level || 0) - minLevel;
            const prev = index > 0 ? (entries[index - 1].level || 0) - minLevel : 0;
            return {
                title: entry.title,
                // 超過上一層深度就往下掉一層就好，避免出現跳級的空層
                level: Math.max(0, Math.min(normalized, index === 0 ? 0 : prev + 1)),
                dest: entry.page,
                ref: ctx.nextRef(),
                childIndexes: [],
            };
        });

        // 用堆疊把節點掛到正確的父層：stack 的最後一個是「目前的父節點」。
        // 只回到 stack[level-1] 上面，所以不會產生空的父層；level 0 就是頂層。
        const roots = [];
        const stack = []; // stack[i] = 第 i 層目前開啟的節點
        items.forEach((item) => {
            stack.length = Math.min(stack.length, item.level);
            if (item.level > 0 && stack.length === item.level) {
                stack[item.level - 1].childIndexes.push(item);
            } else {
                roots.push(item);
            }
            stack[item.level] = item;
        });

        const outlineRef = ctx.nextRef();
        const descendants = (item) => item.childIndexes.reduce((n, c) => n + 1 + descendants(c), 0);

        const write = (item, parentRef, prevRef, nextRef) => {
            const dict = {
                Title: pdfTextString(item.title, PDFString, PDFHexString),
                Parent: parentRef,
                // 目標頁在成品裡可能被旋轉，用 'Fit' 讓閱讀器自己算，最可預期
                Dest: [item.dest.ref, 'Fit'],
            };
            if (prevRef) dict.Prev = prevRef;
            if (nextRef) dict.Next = nextRef;
            if (item.childIndexes.length > 0) {
                dict.First = item.childIndexes[0].ref;
                dict.Last = item.childIndexes[item.childIndexes.length - 1].ref;
                dict.Count = descendants(item);
            }
            ctx.assign(item.ref, ctx.obj(dict));
        };

        const linkList = (siblings, parentRef) => {
            siblings.forEach((item, index) => {
                write(item, parentRef, siblings[index - 1] ? siblings[index - 1].ref : null,
                    siblings[index + 1] ? siblings[index + 1].ref : null);
                if (item.childIndexes.length > 0) linkList(item.childIndexes, item.ref);
            });
        };
        linkList(roots, outlineRef);

        ctx.assign(outlineRef, ctx.obj({
            Type: 'Outlines',
            First: roots[0].ref,
            Last: roots[roots.length - 1].ref,
            Count: roots.reduce((n, r) => n + 1 + descendants(r), 0),
        }));
        return outlineRef;
    }

    async function generatePDF() {
        if (typeof PDFLib === 'undefined' || typeof PDFLib.PDFDocument === 'undefined') {
            console.error("PDFLib not available in generatePDF");
            showNotification("錯誤：無法生成 PDF，編輯函式庫載入失敗。", 'error');
            return;
        }

        // degrees 用來把「來源旋轉 + 使用者旋轉」寫進輸出頁
        const { PDFDocument, rgb, StandardFonts, PDFName, degrees } = PDFLib;

        const pageItems = selectedPages.filter(p => p && p.type !== 'divider');
        if (pageItems.length === 0) {
            progress.textContent = '⚠️ 請至少選擇一個頁面';
            progress.classList.add('active', 'error');
            setTimeout(() => progress.classList.remove('active', 'error'), 3000);
            return;
        }

        try {
            progress.textContent = '⏳ 正在準備生成 PDF...';
            progress.classList.remove('success', 'error');
            progress.classList.add('active');
            
            let newPdf = await PDFDocument.create();
            let customFont;
            let asciiFont;
            let cjkFontAvailable = true;

            // 建立 PDF-Lib 文件快取
            const pdfLibDocCache = new Map();

            try {
                progress.textContent = '正在載入中文字型...';
                const fontUrl = './fonts/NotoSansTC-Regular.ttf';
                const fontBytes = await fetch(fontUrl).then(res => {
                    if (!res.ok) throw new Error(`字型檔案 (${fontUrl}) 載入失敗！ status: ${res.status}`);
                    return res.arrayBuffer();
                });
                
                if (typeof fontkit === 'undefined') throw new Error("fontkit 函式庫載入失敗");
                newPdf.registerFontkit(fontkit); 
                customFont = await newPdf.embedFont(fontBytes);
                // ASCII 用標準字型（pdf-lib 對 CJK 字型的 ASCII 子集化會產生錯誤 glyph 對映）
                asciiFont = await newPdf.embedFont(StandardFonts.Helvetica);
            } catch (fontError) {
                console.error("中文字型載入失敗:", fontError);
                cjkFontAvailable = false;
                showNotification('警告：無法載入中文字型，目錄中的中文會以「?」呈現。', 'error');
                try {
                    // customFont 保持 undefined：要不要用 CJK 字型由 cjkFontAvailable 決定，
                    // 呼叫端一律傳 cjkFontAvailable ? customFont : asciiFont。
                    asciiFont = await newPdf.embedFont(StandardFonts.Helvetica);
                } catch (embedError) {
                    console.error("Failed to embed fallback font:", embedError);
                    showNotification("致命錯誤：無法嵌入預設字型。", 'error');
                    progress.textContent = '❌ 生成失敗：無法嵌入字型';
                    progress.classList.add('active', 'error');
                    return;
                }
            }
            
            // 沒有中文字型時 cjkFont 就等於 asciiFont，並由 cjkFontAvailable 讓文字先被換成 '?'。
            // 只判斷一次，避免每個呼叫點各寫一次三元式（漏掉任何一個就會踩到 undefined 字型）。
            const cjkFont = cjkFontAvailable ? customFont : asciiFont;

            const addToc = addTocCheckbox.checked;
            const addMarks = !!(document.getElementById('addMarksCheckbox') || {}).checked;
            const layoutConfig = readLayoutConfig();
            const impositionConfig = readImpositionConfig();
            const splitConfig = readSplitConfig();
            const addBookmarks = !!(document.getElementById('addBookmarksCheckbox') || {}).checked;

            // --- 合併內容頁 ---
            // 先合併、再畫目錄：目錄的頁碼與超連結必須對應「真的進了輸出」的內容頁。
            // 若某頁複製失敗還照樣印一行，後面所有頁碼與超連結都會整體位移。
            let contentEntries = []; // 依 selectedPages 順序，只放成功複製的內容頁
            let pageCounterForContent = 0;

            for (const item of selectedPages) {
                if (!item || item.type === 'divider') continue;
                pageCounterForContent++;
                progress.textContent = `正在合併頁面 (${pageCounterForContent}/${pageItems.length})...`;
                if (item.type === 'blank') {
                    const prev = contentEntries[contentEntries.length - 1];
                    contentEntries.push({ item, page: addBlankPdfPage(newPdf, prev && prev.page, item.rotation) });
                    continue;
                }

                if (item.fileIndex === undefined || item.fileIndex === null || !pdfFiles[item.fileIndex] || !pdfFiles[item.fileIndex].file || !item.pageNum) {
                    console.error("Missing data for page item:", item);
                    showNotification(`⚠️ 略過 1 頁：來源資料不完整（${item.firstLine || item.pageNum || '未知頁面'}）`, 'error');
                    continue;
                }

                const sourceFile = pdfFiles[item.fileIndex];

                try {
                    let sourcePdf;
                    if (pdfLibDocCache.has(item.fileIndex)) {
                        sourcePdf = pdfLibDocCache.get(item.fileIndex);
                    } else {
                        const freshArrayBuffer = await sourceFile.file.arrayBuffer();
                        // throwOnInvalidObject:false：公部門／掃描件的 PDF 常有小瑕疵，
                        // 用預設值會整份載入失敗，這裡盡量搶救可用的頁面。
                        sourcePdf = await PDFDocument.load(freshArrayBuffer, {
                            ignoreEncryption: true,
                            updateMetadata: false,
                            throwOnInvalidObject: false,
                        });
                        pdfLibDocCache.set(item.fileIndex, sourcePdf);
                    }

                    if (item.pageNum < 1 || item.pageNum > sourcePdf.getPageCount()) {
                        console.error(`Invalid page ${item.pageNum} for ${sourceFile.name}`);
                        showNotification(`⚠️ 略過「${sourceFile.name}」第 ${item.pageNum} 頁：頁碼超出範圍`, 'error');
                        continue;
                    }

                    const [copiedPage] = await newPdf.copyPages(sourcePdf, [item.pageNum - 1]);
                    // copyPages 會保留來源角度，疊加使用者旋轉
                    const existingRotation = copiedPage.getRotation().angle;
                    let newPage = newPdf.addPage(copiedPage);

                    const userRotation = item.rotation || 0;
                    const totalRotation = (existingRotation + userRotation) % 360;

                    newPage.setRotation(degrees(totalRotation));

                    // 裁切先做（只改頁面框，不動內容）。統一尺寸在內容合併完、目錄排版前處理，
                    // 見 scalePageToSize 的說明。
                    if (layoutConfig.fit !== 'original') applyCropBox(newPage, item, layoutConfig);

                    contentEntries.push({ item, page: newPage });
                } catch (loadError) {
                    console.error(`Error loading/copying page ${item.pageNum} from ${sourceFile.name}:`, loadError);
                    showNotification(`錯誤：無法處理檔案 "${sourceFile.name}" 第 ${item.pageNum} 頁。`, 'error');
                }
            }

            // --- 統一頁面尺寸 ---
            // 放在這裡的原因：裁切已經套用、目錄頁還沒排版。頁面物件不變，
            // 所以後續的目錄超連結、書籤、頁碼與浮水印都會落在正確的位置。
            if (layoutConfig.targetSize && contentEntries.length > 0) {
                progress.textContent = '正在統一頁面尺寸...';
                for (const { page } of contentEntries) {
                    try {
                        scalePageToSize(page, layoutConfig.targetSize, PDFLib.concatTransformationMatrix,
                            PDFLib.pushGraphicsState, PDFLib.popGraphicsState);
                    } catch (sizeError) {
                        console.error('統一頁面尺寸失敗，保留原尺寸：', sizeError);
                    }
                }
            }

            // --- 一頁多張／騎馬釘拼版 ---
            // 放在目錄排版之前：拼版會重建頁面並移除原頁面，之後的目錄超連結、
            // 書籤、頁碼與浮水印都會落在拼版後的紙張上。
            // itemEntries：每個成品項目一筆；contentEntries：輸出的實體頁（拼版後是紙張）。
            // sheetIndex 把前者對到後者，目錄頁碼、超連結、書籤與頁碼才不會錯位。
            const itemEntries = contentEntries;
            itemEntries.forEach((entry, index) => { entry.sheetIndex = index; });
            if (impositionConfig.enabled && contentEntries.length > 0) {
                itemEntries.forEach(entry => { entry.sheetIndex = undefined; });
                progress.textContent = '正在拼版...';
                try {
                    contentEntries = await buildImposition(newPdf, contentEntries, impositionConfig, { rgb, PDFDocument });
                } catch (impError) {
                    console.error('拼版失敗，保留原頁面：', impError);
                    contentEntries = itemEntries;
                    itemEntries.forEach((entry, index) => { entry.sheetIndex = index; });
                    showNotification('⚠️ 拼版失敗，已保留原頁面。', 'error');
                }
            }

            // --- 大綱項目（目錄頁與書籤共用同一份）---
            // 只列「有被當成標題的頁」與小節標題。若每一頁都列，200 頁的文件會得到
            // 200 個書籤——那是一份清單，不是目錄。
            const outlineEntries = [];
            {
                let contentIdx = 0;
                for (const item of selectedPages) {
                    if (!item) continue;
                    if (item.type === 'divider') {
                        outlineEntries.push({ kind: 'divider', title: item.firstLine || 'New Section', level: 0, contentIndex: -1 });
                    } else if (contentIdx < itemEntries.length) {
                        const contentItem = itemEntries[contentIdx].item;
                        const sheetIndex = itemEntries[contentIdx].sheetIndex;
                        const autoTitle = `Page ${contentItem.pageNum || '?'}`;
                        const title = contentItem.firstLine || autoTitle;
                        // 有自訂標題（來自目錄編輯或來源書籤）才算一個大綱節點
                        if (title && title !== autoTitle) {
                            outlineEntries.push({
                                kind: 'page',
                                title,
                                level: 1 + (contentItem.level || 0),
                                contentIndex: sheetIndex === undefined ? -1 : sheetIndex,
                            });
                        }
                        contentIdx++;
                    }
                }
                while (outlineEntries.length > 0 && outlineEntries[outlineEntries.length - 1].kind === 'divider') outlineEntries.pop();
            }

            // --- 建立目錄頁 (TOC) ---
            const tocLinkData = [];
            let tocPageCount = 0;

            if (addToc) {
                progress.textContent = '正在建立目錄頁...';

                // 沒有中文字型時，Helvetica 遇到中文會直接丟錯（該行會整條消失），
                // 所以先換成 '?' 再量寬度與繪製，行為與實際輸出一致。
                const enc = cjkFontAvailable ? (t) => String(t) : sanitizeWinAnsi;

                const readSize = (id, fallback) => {
                    const v = parseInt(document.getElementById(id).value, 10);
                    return Number.isFinite(v) ? v : fallback;
                };
                const TOC_CONFIG = {
                    MAIN_TITLE_SIZE: readSize('tocMainTitleSize', 20),
                    SECTION_TITLE_SIZE: readSize('tocSectionSize', 14),
                    ITEM_TITLE_SIZE: readSize('tocItemTitleSize', 12),
                    ITEM_PAGENUM_SIZE: readSize('tocPageNumSize', 12),
                    LINE_HEIGHT: readSize('tocLineHeight', TOC_LINE_HEIGHT_DEFAULT)
                };

                const tocItems = outlineEntries;

                // 先模擬排版計算目錄總頁數，否則跨頁目錄的頁碼會少算
                let simY = 595 - 90;
                let totalTocPages = 1;
                for (const item of tocItems) {
                    if (simY < 50) { totalTocPages++; simY = 595 - 90; }
                    simY -= (item.kind === 'divider') ? 35 : TOC_CONFIG.LINE_HEIGHT;
                }

                // 內容頁已經先合併進文件，目錄頁必須插到最前面（不能 addPage，那會排到最後）。
                // 注意要插在「已插入的目錄頁之後」，否則第二張目錄會跑到第一張前面，
                // 整份目錄的順序就反了（頁碼與超連結看起來沒錯，實際內容是倒的）。
                let tocInsertIndex = 0;
                const newTocPage = () => {
                    const p = newPdf.insertPage(tocInsertIndex, [842, 595]); // 橫向 A4
                    tocInsertIndex++;
                    tocPageCount++;
                    drawMixedText(p, tocPageCount === 1 ? '目錄' : '目錄 (續)', 50, 595 - 50,
                        TOC_CONFIG.MAIN_TITLE_SIZE, cjkFontAvailable ? customFont : asciiFont, asciiFont, rgb(0, 0, 0),
                        { cjkFontAvailable });
                    return p;
                };

                let tocPage = newTocPage();
                let yPosition = 595 - 90;

                for (const tocItem of tocItems) {
                    // TOC 換頁處理
                    if (yPosition < 50) {
                        tocPage = newTocPage();
                        yPosition = 595 - 90;
                    }

                    // 目錄頁的縮排：每層 14pt，最多縮到剩 120pt 的標題空間
                    const indent = Math.min((tocItem.level || 0) * 14, 120);

                    if (tocItem.kind === 'divider') {
                        yPosition -= 10;
                        drawMixedText(tocPage, enc(tocItem.title), 50 + indent, yPosition, TOC_CONFIG.SECTION_TITLE_SIZE,
                            cjkFontAvailable ? customFont : asciiFont, asciiFont, rgb(0, 0, 0),
                            { cjkFontAvailable });
                        yPosition -= 25;
                        continue;
                    }

                    const title = enc(tocItem.title);
                    const pageNumStr = `${tocItem.contentIndex + 1 + totalTocPages}`;

                    const leftMargin = 70 + indent;
                    const rightMargin = 50;
                    const pageContentWidth = tocPage.getWidth() - leftMargin - rightMargin;

                    let pageNumWidth = 0;
                    try { pageNumWidth = mixedTextWidth(pageNumStr, TOC_CONFIG.ITEM_PAGENUM_SIZE, customFont, asciiFont); } catch (e) {}

                    let truncatedTitle = title;
                    let titleWidth = 0;
                    try { titleWidth = mixedTextWidth(truncatedTitle, TOC_CONFIG.ITEM_TITLE_SIZE, customFont, asciiFont); } catch (e) {}

                    // 截斷迴圈：每次真的少一個字（先去掉上一次加的省略號再加回），
                    // 原本的 slice(0,-2)+'…' 長度可能不減反增，最壞情況會多繞很多圈。
                    const minDotSpace = 20;
                    while (titleWidth > 0 && pageContentWidth > 0 && (titleWidth + pageNumWidth + minDotSpace > pageContentWidth) && truncatedTitle.length > 5) {
                        truncatedTitle = truncatedTitle.replace(/…$/, '').slice(0, -1) + '…';
                        try { titleWidth = mixedTextWidth(truncatedTitle, TOC_CONFIG.ITEM_TITLE_SIZE, customFont, asciiFont); } catch (e) { titleWidth = 0; }
                    }

                    // 繪製標題
                    drawMixedText(tocPage, truncatedTitle, leftMargin, yPosition, TOC_CONFIG.ITEM_TITLE_SIZE,
                        cjkFontAvailable ? customFont : asciiFont, asciiFont, rgb(0, 0, 0), { cjkFontAvailable });

                    // 繪製頁碼
                    drawMixedText(tocPage, pageNumStr, tocPage.getWidth() - rightMargin - pageNumWidth, yPosition, TOC_CONFIG.ITEM_PAGENUM_SIZE, customFont, asciiFont, rgb(0, 0, 0));

                    // 繪製點點
                    let dotWidth = 0;
                    const dotSize = Math.min(TOC_CONFIG.ITEM_TITLE_SIZE, TOC_CONFIG.ITEM_PAGENUM_SIZE);
                    try { dotWidth = asciiFont.widthOfTextAtSize('.', dotSize); } catch (e) {}

                    if (dotWidth > 0) {
                        const dotStartX = leftMargin + titleWidth + 5;
                        const dotEndX = tocPage.getWidth() - rightMargin - pageNumWidth - 5;
                        const availableDotSpace = dotEndX - dotStartX;
                        if (availableDotSpace > dotWidth) {
                            const numDots = Math.floor(availableDotSpace / dotWidth);
                            const dotString = '.'.repeat(numDots);
                            tocPage.drawText(dotString, {
                                x: dotStartX, y: yPosition,
                                size: dotSize, font: asciiFont, color: rgb(0, 0, 0), opacity: 0.5
                            });
                        }
                    }

                    // 儲存連結資訊
                    tocLinkData.push({
                        tocPage: tocPage,
                        targetContentPageIndex: tocItem.contentIndex,
                        linkRect: {
                            x: leftMargin - 5,
                            y: yPosition - 2,
                            width: pageContentWidth + 10,
                            height: Math.max(TOC_CONFIG.ITEM_TITLE_SIZE, TOC_CONFIG.ITEM_PAGENUM_SIZE) + 4
                        }
                    });

                    yPosition -= TOC_CONFIG.LINE_HEIGHT;
                }
            }

            // --- 頁面標記：頁首／頁尾／頁碼 ---
            // 三者共用變數替換與位置計算，所以在這裡一次畫完。
            // 時機：目錄頁數已確定（{total} 要含目錄頁）、且在其他內容之後，標記才不會被蓋掉。
            //
            // {n} 支援分節編號（正文用 1,2,3、附錄用 i,ii,iii）。規則刻意保持單純可預期：
            //   * 編號在每個小節的第一頁重新起算
            //   * 沒有小節時整份連續編號（與原本行為相同）
            //   * 編號方案由目錄編輯器的行首標記指定（例如 [roman-lower]），同一小節內沿用，
            //     後續頁面不需要重複標記
            // 有沒有小節，決定 {n} 的語意：
            //   沒有小節 → 整份連續編號，且含目錄頁位移（與這功能加入前的行為一致）
            //   有小節   → 每個小節重新起算，並依該小節指定的方案編號
            // 這樣「原本就用得好好的文件」不會因為新功能而改變頁碼。
            const hasSections = selectedPages.some(item => item && item.type === 'divider');
            {
                let section = 0;
                let contentIdx = 0;
                let inheritedScheme = null;
                for (const item of selectedPages) {
                    if (!item) continue;
                    if (item.type === 'divider') { section++; continue; }
                    if (contentIdx >= itemEntries.length) break;
                    const entry = itemEntries[contentIdx];
                    entry.restartKey = hasSections ? `s${section}` : 'all';
                    if (item.scheme) inheritedScheme = item.scheme;
                    entry.scheme = inheritedScheme;
                    contentIdx++;
                }
            }
            // 拼版後一張紙沿用它第一個頁面的分節與編號方案
            if (contentEntries !== itemEntries) {
                const firstOnSheet = new Map();
                for (const entry of itemEntries) {
                    if (entry.sheetIndex !== undefined && !firstOnSheet.has(entry.sheetIndex)) firstOnSheet.set(entry.sheetIndex, entry);
                }
                contentEntries.forEach((sheet, index) => {
                    const first = firstOnSheet.get(index);
                    sheet.restartKey = first ? first.restartKey : undefined;
                    sheet.scheme = first ? first.scheme : null;
                });
            }
            const pageLabels = computePageLabels(contentEntries);
            // 沒有小節時，{n} 維持「含目錄頁位移的實體頁序」
            if (!hasSections) {
                contentEntries.forEach((entry, index) => { pageLabels[index] = String(index + 1 + tocPageCount); });
            }

            if (addMarks) {
                const marks = readMarksConfig();
                if (marks.length > 0) {
                    progress.textContent = '正在加上頁面標記...';
                    const totalPages = contentEntries.length + tocPageCount;
                    const today = new Date();
                    const pad2 = (n) => String(n).padStart(2, '0');
                    const dateText = `${today.getFullYear()}-${pad2(today.getMonth() + 1)}-${pad2(today.getDate())}`;
                    contentEntries.forEach(({ page, item }, index) => {
                        const { width, height } = page.getSize();
                        if (!(width > 0 && height > 0)) return;
                        for (const mark of marks) {
                            const label = formatPageNumber(mark.format, {
                                // {n} 用分節編號（未指定方案時就是 1,2,3...）
                                n: pageLabels[index] !== undefined ? pageLabels[index] : index + 1 + tocPageCount,
                                // {total} 含目錄頁：使用者看到的實體總頁數
                                total: totalPages,
                                name: item.fileName || '',
                                date: dateText,
                            });
                            if (!label) continue;
                            const textWidth = watermarkTextWidth(label, mark.size, cjkFont, asciiFont);
                            const { x, y } = pageNumberPositionXY(mark.position, width, height, textWidth, mark.margin);
                            try {
                                drawMixedText(page, label, x, y, mark.size, cjkFont, asciiFont, rgb(0, 0, 0),
                                    { cjkFontAvailable });
                            } catch (markError) {
                                console.error(`頁面標記（${mark.key}）繪製失敗：`, markError);
                            }
                        }
                    });
                }
            }

            // --- 浮水印／印章 ---
            // 畫在所有內容與頁碼之後：浮水印要蓋在最上層才不會被後面的繪製蓋掉。
            // 頁碼範圍以「成品內容頁」為準（不含目錄頁），跟使用者在右側看到的順序一致。
            const addWatermark = !!(document.getElementById('addWatermarkCheckbox') || {}).checked;

            if (addWatermark && contentEntries.length > 0) {
                const wmConfig = readWatermarkConfig();
                if (wmConfig.enabled) {
                    progress.textContent = '正在加上浮水印...';
                    const targets = parsePageRange(document.getElementById('watermarkRange').value, contentEntries.length);
                    const wmFonts = { centerFont: cjkFont, asciiFont, rgb, degrees };
                    contentEntries.forEach(({ page }, index) => {
                        if (!targets.has(index + 1)) return;
                        try {
                            // 用 q/Q 包住：透明度等圖形狀態不會外洩到後續繪製
                            page.pushOperators(PDFLib.pushGraphicsState());
                            drawWatermarkOnPage(page, wmConfig, wmFonts);
                            page.pushOperators(PDFLib.popGraphicsState());
                        } catch (wmError) {
                            console.error('浮水印繪製失敗：', wmError);
                        }
                    });
                }
            }

            // --- 寫入書籤大綱（閱讀器左側的原生導覽）---
            // 來源：目前右側成品的實際順序與最終標題（含目錄編輯器的修改）。
            if (addBookmarks && contentEntries.length > 0) {
                progress.textContent = '正在寫入書籤大綱...';
                try {
                    const bookmarkEntries = [];

                    // 與目錄頁共用同一份 outlineEntries：標題與階層都取自目錄的最終結果
                    // （包含在目錄編輯器裡改過的標題），兩者永遠一致。
                    for (const entry of outlineEntries) {
                        bookmarkEntries.push({
                            title: entry.title,
                            level: entry.level || 0,
                            page: entry.contentIndex >= 0 ? contentEntries[entry.contentIndex].page : null,
                        });
                    }

                    // 沒有目標頁的項目不能當書籤（跳不過去）；小節標題若底下沒有
                    // 可用項目，留著只會變成點不動的空節點，所以一併移除。
                    const usable = bookmarkEntries.filter((entry, index) => {
                        if (entry.page) return true;
                        return bookmarkEntries.slice(index + 1).some(next => next.page);
                    });
                    if (usable.length > 0) {
                        const { PDFString, PDFHexString } = PDFLib;
                        const outlineRef = buildOutlineTree(newPdf, usable, PDFString, PDFHexString);
                        newPdf.catalog.set(PDFName.of('Outlines'), outlineRef);
                        newPdf.catalog.set(PDFName.of('PageMode'), PDFName.of('UseOutlines'));
                    }
                } catch (outlineError) {
                    console.error('寫入書籤大綱失敗：', outlineError);
                    showNotification('⚠️ 書籤大綱寫入失敗，其餘內容仍會正常輸出。', 'error');
                }
            }

            // --- 建立目錄超連結 ---
            // 內容頁已經先合併完，目錄頁數也確定，所以連結目標＝目錄頁數＋內容頁序，
            // 一定有對應的頁面（不再需要用「索引超界就跳過」來掩蓋位移）。
            if (addToc && tocLinkData.length > 0) {
                progress.textContent = '正在建立目錄超連結...';
                const allPages = newPdf.getPages();

                for (const linkInfo of tocLinkData) {
                    const targetPageIndex = tocPageCount + linkInfo.targetContentPageIndex;
                    if (targetPageIndex >= allPages.length) continue;
                    const targetPage = allPages[targetPageIndex];
                    const rect = linkInfo.linkRect;
                    try {
                        // 1) 先建立 annotation 並註冊，取得 PDFRef
                        const annotRef = newPdf.context.register(newPdf.context.obj({
                            Type: 'Annot', Subtype: 'Link',
                            Rect: [rect.x, rect.y, rect.x + rect.width, rect.y + rect.height],
                            Border: [0, 0, 0], C: [0, 0, 1],
                            A: { S: 'GoTo', D: [targetPage.ref, 'Fit'] }
                        }));
                        // 2) 用節點 API 掛上去：addAnnot 內部會 lookupMaybe 既有的 Annots 陣列，
                        //    沒有就建立，直接 push 未註冊的物件或寫回 ref 都不保險。
                        if (typeof linkInfo.tocPage.node.addAnnot === 'function') {
                            linkInfo.tocPage.node.addAnnot(annotRef);
                        } else {
                            let annots = linkInfo.tocPage.node.lookup(PDFName.of('Annots'));
                            if (!annots || typeof annots.push !== 'function') {
                                annots = newPdf.context.obj([]);
                                linkInfo.tocPage.node.set(PDFName.of('Annots'), annots);
                            }
                            annots.push(annotRef);
                        }
                    } catch (linkError) {
                        console.error(`無法建立超連結 (目標頁 ${targetPageIndex + 1}):`, linkError);
                    }
                }
            }

            // --- 依小節拆成多檔（打包 ZIP 下載）---
            // 放在所有單檔後處理之後：拆檔用的是同一組設定（裁切／統一尺寸／標記），
            // 這樣「預覽看到的單檔」與「拆出來的檔案」內容才會一致。
            if (splitConfig.enabled && contentEntries.length > 0) {
                progress.textContent = '正在拆成多檔...';
                const groups = groupPagesBySection(selectedPages);
                const pad2s = (v) => String(v).padStart(2, '0');
                const today2 = new Date();
                const dateText = `${today2.getFullYear()}-${pad2s(today2.getMonth() + 1)}-${pad2s(today2.getDate())}`;
                const names = buildSplitFileNames(groups, splitConfig, dateText);
                const marksConfig = addMarks ? readMarksConfig() : [];
                const zipped = [];
                for (let i = 0; i < groups.length; i++) {
                    progress.textContent = `正在產生第 ${i + 1}/${groups.length} 個檔案...`;
                    try {
                        const bytes = await buildSplitPdf(groups[i].items, {
                            layoutConfig, marksConfig, splitIndex: i + 1, splitTotal: groups.length,
                            dateText, customFont, asciiFont, cjkFont, cjkFontAvailable,
                        });
                        zipped.push({ name: names[i], data: bytes });
                    } catch (splitError) {
                        console.error(`產生「${names[i]}」失敗：`, splitError);
                    }
                }
                if (zipped.length === 0) {
                    showNotification('⚠️ 拆檔沒有產生任何檔案。', 'error');
                } else {
                    const zipBytes = buildZip(zipped);
                    lastExportZip = zipBytes;
                    const zipName = `重組後的PDF_${dateText}_${zipped.length}檔.zip`;
                    downloadBlob(new Blob([zipBytes], { type: 'application/zip' }), zipName);
                    showNotification(`✅ 已產生 ${zipped.length} 個檔案（ZIP）`, 'success');
                }
                finalPdfBytes = await newPdf.save();
                progress.textContent = zipped.length > 0 ? '✅ 拆檔完成！' : '❌ 拆檔失敗';
                progress.classList.add(zipped.length > 0 ? 'success' : 'error');
                setTimeout(() => progress.classList.remove('active', 'success', 'error'), 5000);
                return; // 拆檔模式下不再走預覽流程
            }

            progress.textContent = '正在儲存 PDF...';
            
            let pdfBytes = await newPdf.save();
            finalPdfBytes = pdfBytes;
            
            const blob = new Blob([finalPdfBytes], { type: 'application/pdf' });
            // 前一次預覽的 blob 要先釋放：對話框已經開啟時 showModal() 不會觸發 close，
            // 舊 URL 就永遠沒人回收（連續生成會一份一份累積，每份都是一整本 PDF）。
            if (currentPreviewUrl) { URL.revokeObjectURL(currentPreviewUrl); currentPreviewUrl = null; }
            currentPreviewUrl = URL.createObjectURL(blob);

            document.getElementById('previewFrame').src = currentPreviewUrl;
            previewModal.showModal();
            
            progress.textContent = '✅ 預覽生成成功！';
            progress.classList.add('success');
            setTimeout(() => progress.classList.remove('active', 'success'), 5000);

        } catch (error) {
            console.error('生成 PDF 時發生錯誤：', error);
            const errMsg = (error && error.message) ? error.message : String(error);
            progress.textContent = '❌ 生成失敗：' + errMsg;
            showNotification('❌ 生成失敗：' + errMsg, 'error');
            progress.classList.add('active', 'error');
            setTimeout(() => progress.classList.remove('active', 'error'), 8000);
        }
    }
};
