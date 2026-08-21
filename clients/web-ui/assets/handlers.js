// @ts-check
// The interface's own handlers, moved out of index.html.
//
// WHY THEY MOVED. index.html declared ~230 inline on* attributes. Every one
// was ours, written into the file with literal arguments, and none was
// reachable by remote content — so they were never the vulnerability. They
// were what a strict Content-Security-Policy would have taken down with it,
// and while they were there `script-src` had to carry 'unsafe-inline',
// which is precisely the permission that makes an INJECTED script or a
// javascript: URI run. Moving them earns `script-src 'self'`, and after
// that an injection has nothing to execute with.
//
// WHAT DID NOT CHANGE. Each body below is the same code that was in the
// attribute, in the same order, bound to the same element for the same
// event — bindHandlers attaches one listener per element, which is exactly
// what an inline attribute does, so nothing about when a handler fires
// moves. `this` became `el` because that is what `this` meant there.
//
// THE THREE THAT NEEDED TRANSLATING, because an inline handler's RETURN
// VALUE meant something and addEventListener ignores it:
//
//     return say(event)                    -> preventDefault when false
//     if (mentionsOnKeydown(event)) return false
//                                          -> preventDefault + stopPropagation
//     openInviteFromPass();return false    -> preventDefault
//
// Copying those verbatim would have left three controls that look bound and
// quietly do half their job — a submit that submits anyway, an arrow key
// that also scrolls.
//
// The index is positional and generated: an attribute says data-on-click="42"
// and entry 42 is its body. Editing one means editing the other.

const UI_HANDLERS = [
  /*   0 click    */ (event, el) => { togglePanel('nav'); },
  /*   1 click    */ (event, el) => { dlgLan.showModal(); },
  /*   2 click    */ (event, el) => { openWizard(); },
  /*   3 click    */ (event, el) => { openJoinDialog(); },
  /*   4 click    */ (event, el) => { openDiscover(); },
  /*   5 click    */ (event, el) => { openSignals(); },
  /*   6 click    */ (event, el) => { openGateway(); },
  /*   7 click    */ (event, el) => { openSettings(); },
  /*   8 click    */ (event, el) => { showIdentity(); },
  /*   9 click    */ (event, el) => { switchView('chat'); },
  /*  10 click    */ (event, el) => { switchView('posts'); },
  /*  11 click    */ (event, el) => { switchView('shelf'); },
  /*  12 click    */ (event, el) => { copyPublicLink(); },
  /*  13 click    */ (event, el) => { openComposer(null,''); },
  /*  14 click    */ (event, el) => { CAT.openAddSpace(); },
  /*  15 click    */ (event, el) => { openCustomize(); },
  /*  16 click    */ (event, el) => { openResPaletteEditor(); },
  /*  17 click    */ (event, el) => { openPassCurrent(); },
  /*  18 click    */ (event, el) => { toggleMembers(); },
  /*  19 submit   */ (event, el) => { if (say(event) === false) event.preventDefault(); },
  /*  20 click    */ (event, el) => { toggleAttachMenu(event); },
  /*  21 click    */ (event, el) => { attachPick('photo'); },
  /*  22 click    */ (event, el) => { attachPick('video'); },
  /*  23 click    */ (event, el) => { attachPick('audio'); },
  /*  24 click    */ (event, el) => { attachPick('doc'); },
  /*  25 click    */ (event, el) => { toggleVoice(); },
  /*  26 input    */ (event, el) => { growComposer(el); mentionsOnInput(event); mentionsRenderChips(); },
  /*  27 keydown  */ (event, el) => {
    if (mentionsOnKeydown(event)) { event.preventDefault(); event.stopPropagation(); return; }
    // Enter sends, Shift+Enter makes a line. The form's submit button no
    // longer gets Enter for free now that this is a textarea, and a
    // messenger where Enter inserts a newline is a messenger nobody can
    // send from. IME composition is left alone: Enter mid-composition is
    // choosing a candidate, not sending.
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      const form = el.closest('form');
      if (form) form.requestSubmit ? form.requestSubmit() : form.dispatchEvent(new Event('submit', { cancelable: true }));
    }
  },
  /*  28 blur     */ (event, el) => { mentionsClosePicker(); },
  /*  29 click    */ (event, el) => { presenceToggle(); },
  /*  30 keydown  */ (event, el) => { presenceBtnKey(event); },
  /*  31 click    */ (event, el) => { cancelVoice(); },
  /*  32 click    */ (event, el) => { toggleVoice(); },
  /*  33 click    */ (event, el) => { joinCurrentPublic(); },
  /*  34 change   */ (event, el) => { onFilesPicked(Array.from(el.files)); el.value = ''; },
  /*  35 click    */ (event, el) => { SHARE.cancel(); },
  /*  36 click    */ (event, el) => { SHARE.send(); },
  /*  37 click    */ (event, el) => { SHARE.closeReport(); },
  /*  38 click    */ (event, el) => { CAT.back(); },
  /*  39 click    */ (event, el) => { CAT.openDirectories(); },
  /*  40 click    */ (event, el) => { CAT.addDirectory(); },
  /*  41 click    */ (event, el) => { dlgDirs.close(); },
  /*  42 click    */ (event, el) => { closeSignals(); },
  /*  43 click    */ (event, el) => { qrSetFilter('all'); },
  /*  44 click    */ (event, el) => { qrSetFilter('priority'); },
  /*  45 click    */ (event, el) => { qrSetFilter('digest'); },
  /*  46 click    */ (event, el) => { qrMarkAllSeen(); },
  /*  47 click    */ (event, el) => { sendAttachment(); },
  /*  48 click    */ (event, el) => { pendingBatch = []; dlgAttach.close(); },
  /*  49 click    */ (event, el) => { sendVoice(); },
  /*  50 click    */ (event, el) => { dlgVoiceReview.close(); },
  /*  51 click    */ (event, el) => { dlgViewer.close(); },
  /*  52 click    */ (event, el) => { sendLink(); },
  /*  53 click    */ (event, el) => { dlgLink.close(); },
  /*  54 click    */ (event, el) => { CAT.lookUpSpace(); },
  /*  55 click    */ (event, el) => { CAT.addSpaceToDirectory(); },
  /*  56 click    */ (event, el) => { dlgAddSpace.close(); },
  /*  57 change   */ (event, el) => { renderSignalPreview(); },
  /*  58 click    */ (event, el) => { sendSignal(); },
  /*  59 click    */ (event, el) => { dlgSignal.close(); },
  /*  60 click    */ (event, el) => { wizStep(2); },
  /*  61 click    */ (event, el) => { dlgWiz.close(); },
  /*  62 change   */ (event, el) => { updSeed(); },
  /*  63 change   */ (event, el) => { updSeed(); },
  /*  64 change   */ (event, el) => { updSeed(); },
  /*  65 click    */ (event, el) => { wizStep(1); },
  /*  66 click    */ (event, el) => { wizStep(3); },
  /*  67 click    */ (event, el) => { wizStep(2); },
  /*  68 click    */ (event, el) => { wizStep(4); },
  /*  69 click    */ (event, el) => { pickWizVis('private'); },
  /*  70 click    */ (event, el) => { pickWizVis('public'); },
  /*  71 click    */ (event, el) => { pickWizMode('community'); },
  /*  72 click    */ (event, el) => { pickWizMode('broadcast'); },
  /*  73 click    */ (event, el) => { wizStep(3); },
  /*  74 click    */ (event, el) => { wizCreate(); },
  /*  75 click    */ (event, el) => { setAccessVis('unlisted'); },
  /*  76 click    */ (event, el) => { setAccessVis('public'); },
  /*  77 click    */ (event, el) => { setAccessPurpose(''); },
  /*  78 click    */ (event, el) => { setAccessPurpose('directory'); },
  /*  79 click    */ (event, el) => { setAccessMode('all'); },
  /*  80 click    */ (event, el) => { setAccessMode('curated'); },
  /*  81 click    */ (event, el) => { pickDirectoryWho('me'); },
  /*  82 click    */ (event, el) => { pickDirectoryWho('selected'); },
  /*  83 click    */ (event, el) => { pickDirectoryWho('anyone'); },
  /*  84 click    */ (event, el) => { addCurator(); },
  /*  85 click    */ (event, el) => { setContributionLimit(0); },
  /*  86 click    */ (event, el) => { setContributionLimit(24); },
  /*  87 click    */ (event, el) => { setContributionLimit(16); },
  /*  88 click    */ (event, el) => { setContributionLimit(8); },
  /*  89 click    */ (event, el) => { toggleFreeze(); },
  /*  90 click    */ (event, el) => { toggleMirror(); },
  /*  91 click    */ (event, el) => { toggleSeed(); },
  /*  92 click    */ (event, el) => { dlgAccess.close(); },
  /*  93 click    */ (event, el) => { joinSetMode('link'); },
  /*  94 click    */ (event, el) => { joinSetMode('pass'); },
  /*  95 input    */ (event, el) => { qlOnWordsInput(); },
  /*  96 keydown  */ (event, el) => { if(event.key==='Enter'){event.preventDefault();enterWithLink()} },
  /*  97 click    */ (event, el) => { enterWithLink(); },
  /*  98 click    */ (event, el) => { dlgJoin.close(); },
  /*  99 click    */ (event, el) => { requestEntry(); },
  /* 100 click    */ (event, el) => { apToggleListen(); },
  /* 101 click    */ (event, el) => { dlgJoin.close(); },
  /* 102 click    */ (event, el) => { openJoined(); },
  /* 103 click    */ (event, el) => { resetJoin(); },
  /* 104 click    */ (event, el) => { dlgJoin.close(); },
  /* 105 click    */ (event, el) => { apTogglePlay(); },
  /* 106 click    */ (event, el) => { mintPass(); },
  /* 107 click    */ (event, el) => { copyPass(); },
  /* 108 click    */ (event, el) => { revokeCurrentPass(); },
  /* 109 click    */ (event, el) => { dlgPass.close(); },
  /* 110 click    */ (event, el) => { openInviteFromPass(); event.preventDefault(); },
  /* 111 click    */ (event, el) => { obStep('name'); },
  /* 112 click    */ (event, el) => { dlgWelcome.close(); openJoinDialog(); },
  /* 113 click    */ (event, el) => { dlgWelcome.close(); },
  /* 114 click    */ (event, el) => { obSubmitName(); },
  /* 115 click    */ (event, el) => { dlgWelcome.close(); },
  /* 116 click    */ (event, el) => { obFinish(); },
  /* 117 click    */ (event, el) => { showSettingsCat('appearance'); },
  /* 118 click    */ (event, el) => { showSettingsCat('relay'); },
  /* 119 click    */ (event, el) => { showSettingsCat('radio'); },
  /* 120 click    */ (event, el) => { showSettingsCat('attention'); qrLoadSettings(); },
  /* 121 click    */ (event, el) => { showSettingsCat('ai'); },
  /* 122 click    */ (event, el) => { showSettingsCat('notifications'); notifSyncUI(); unlockSyncUI(); },
  /* 123 click    */ (event, el) => { dlgSettings.close(); openRadioMeet(); },
  /* 124 click    */ (event, el) => { refreshGateway(); },
  /* 125 click    */ (event, el) => { meshConnect(); },
  /* 126 click    */ (event, el) => { setLocale('en');syncSettingsUI(); },
  /* 127 click    */ (event, el) => { setLocale('ru');syncSettingsUI(); },
  /* 128 click    */ (event, el) => { setTheme('auto');syncSettingsUI(); },
  /* 129 click    */ (event, el) => { setTheme('light');syncSettingsUI(); },
  /* 130 click    */ (event, el) => { setTheme('dark');syncSettingsUI(); },
  /* 131 click    */ (event, el) => { setPreset('quiet-glass');syncSettingsUI(); },
  /* 132 click    */ (event, el) => { setPreset('daylight');syncSettingsUI(); },
  /* 133 click    */ (event, el) => { setPreset('minimal-mono');syncSettingsUI(); },
  /* 134 click    */ (event, el) => { setPreset('comfort');syncSettingsUI(); },
  /* 135 click    */ (event, el) => { setStarfield('drift'); },
  /* 136 click    */ (event, el) => { setStarfield('still'); },
  /* 137 click    */ (event, el) => { setStarfield('off'); },
  /* 138 click    */ (event, el) => { setAtmosphere('full'); },
  /* 139 click    */ (event, el) => { setAtmosphere('calm'); },
  /* 140 click    */ (event, el) => { setAtmosphere('poster'); },
  /* 141 click    */ (event, el) => { setAtmosphere('off'); },
  /* 142 click    */ (event, el) => { setAtmosphereSound('ask'); },
  /* 143 click    */ (event, el) => { setAtmosphereSound('remember'); },
  /* 144 click    */ (event, el) => { setAtmosphereSound('never'); },
  /* 145 click    */ (event, el) => { setProtocolView(false); },
  /* 146 click    */ (event, el) => { setProtocolView(true); },
  /* 147 click    */ (event, el) => { setRenderMode('auto'); },
  /* 148 click    */ (event, el) => { setRenderMode('full'); },
  /* 149 click    */ (event, el) => { setRenderMode('reduced'); },
  /* 150 click    */ (event, el) => { setRenderMode('minimal'); },
  /* 151 click    */ (event, el) => { setEffectsMode('full'); },
  /* 152 click    */ (event, el) => { setEffectsMode('subtle'); },
  /* 153 click    */ (event, el) => { setEffectsMode('static'); },
  /* 154 click    */ (event, el) => { setEffectsMode('off'); },
  /* 155 click    */ (event, el) => { setConnMode('auto'); },
  /* 156 click    */ (event, el) => { setConnMode('internet'); },
  /* 157 click    */ (event, el) => { setConnMode('radio'); },
  /* 158 click    */ (event, el) => { setConnMode('offline'); },
  /* 159 click    */ (event, el) => { setRelayMode('automatic'); },
  /* 160 click    */ (event, el) => { setRelayMode('custom'); },
  /* 161 click    */ (event, el) => { saveRelay(); },
  /* 162 click    */ (event, el) => { remeasureRelays(); },
  /* 163 click    */ (event, el) => { copyRelayDiagnostics(); },
  /* 164 click    */ (event, el) => { relayPush(); },
  /* 165 click    */ (event, el) => { relayPull(); },
  /* 166 click    */ (event, el) => { stayPick(false); },
  /* 167 click    */ (event, el) => { stayPick(true); },
  /* 168 click    */ (event, el) => { notifPick('hidden'); },
  /* 169 click    */ (event, el) => { notifPick('space'); },
  /* 170 click    */ (event, el) => { notifPick('preview'); },
  /* 171 click    */ (event, el) => { forgetPassphrase(); },
  /* 172 click    */ (event, el) => { qrPickMode('off'); },
  /* 173 click    */ (event, el) => { qrPickMode('minimal'); },
  /* 174 click    */ (event, el) => { qrPickMode('custom'); },
  /* 175 click    */ (event, el) => { qrSaveSettings(); },
  /* 176 click    */ (event, el) => { qrForget(); },
  /* 177 click    */ (event, el) => { pickProvider('anthropic'); },
  /* 178 click    */ (event, el) => { pickProvider('openai'); },
  /* 179 click    */ (event, el) => { pickProvider('openai-compatible'); },
  /* 180 click    */ (event, el) => { pickProvider('local'); },
  /* 181 click    */ (event, el) => { saveLLM(); },
  /* 182 click    */ (event, el) => { testLLM(); },
  /* 183 click    */ (event, el) => { dlgSettings.close(); },
  /* 184 click    */ (event, el) => { dlgDeleteSpace.close(); },
  /* 185 click    */ (event, el) => { confirmDeleteSpace(); },
  /* 186 click    */ (event, el) => { pickSeg('custMotion','still'); },
  /* 187 click    */ (event, el) => { pickSeg('custMotion','quiet'); },
  /* 188 click    */ (event, el) => { pickSeg('custMotion','lively'); },
  /* 189 click    */ (event, el) => { pickSeg('custDensity','calm'); },
  /* 190 click    */ (event, el) => { pickSeg('custDensity','dense'); },
  /* 191 click    */ (event, el) => { openGenerate(); },
  /* 192 click    */ (event, el) => { dlgCustomize.close(); },
  /* 193 click    */ (event, el) => { applyCustomize(); },
  /* 194 change   */ (event, el) => { onKindChange(); },
  /* 195 click    */ (event, el) => { toggleComposerRail(); },
  /* 196 click    */ (event, el) => { aiDraftPost(); },
  /* 197 click    */ (event, el) => { saveComposerDraft(); },
  /* 198 click    */ (event, el) => { closeComposer(); },
  /* 199 click    */ (event, el) => { publishComposer(); },
  /* 200 click    */ (event, el) => { dlgAppBlock.close(); },
  /* 201 click    */ (event, el) => { confirmAppBlock(); },
  /* 202 click    */ (event, el) => { doGenerate(); },
  /* 203 click    */ (event, el) => { closeGenerate(); },
  /* 204 click    */ (event, el) => { acceptGenerate(); },
  /* 205 click    */ (event, el) => { qlSetKind('just-us'); },
  /* 206 click    */ (event, el) => { qlSetKind('a-few'); },
  /* 207 click    */ (event, el) => { qlSetApproval(''); },
  /* 208 click    */ (event, el) => { qlSetApproval('host'); },
  /* 209 click    */ (event, el) => { mintQuickLink(); },
  /* 210 click    */ (event, el) => { qlCopy(); },
  /* 211 click    */ (event, el) => { saveBackup(); },
  /* 212 click    */ (event, el) => { renameSelf(); },
  /* 213 click    */ (event, el) => { document.getElementById('meTech').style.display='block'; },
  /* 214 click    */ (event, el) => { dlgMe.close(); },
  /* 215 click    */ (event, el) => { confirmRelayTrust(); },
  /* 216 click    */ (event, el) => { dlgRelayTrust.close(); },
  /* 217 click    */ (event, el) => { lanConnect(); },
  /* 218 click    */ (event, el) => { dlgLan.close(); },
  /* 219 click    */ (event, el) => { announceOnRadio(); },
  /* 220 click    */ (event, el) => { refreshRadioMeet(); },
  /* 221 click    */ (event, el) => { dlgRadioMeet.close(); },
  /* 222 click    */ (event, el) => { mintInvite(); },
  /* 223 click    */ (event, el) => { dlgInvite.close(); },
  /* 224 click    */ (event, el) => { saveResPalette(); },
  /* 225 click    */ (event, el) => { dlgResPalette.close(); },
  /* 226 click    */ (event, el) => { confirmListenRoom(); },
  /* 227 click    */ (event, el) => { dlgListenRoom.close(); },
  /* 228 click    */ (event, el) => { confirmKeep(); },
  /* 229 click    */ (event, el) => { dlgKeep.close(); },
  /* 230 click    */ (event, el) => { showSettingsCat('devices'); loadDevices(); loadPasscode(); },
  /* 231 click    */ (event, el) => { beginPairingUI(); },
  /* 232 click    */ (event, el) => { copyPairOffer(); },
  /* 233 click    */ (event, el) => { playPairOffer(); },
  /* 234 click    */ (event, el) => { approvePairingUI(); },
  /* 235 click    */ (event, el) => { cancelPairingUI(); },
  /* 236 click    */ (event, el) => { bindPasscode(); },
  /* 237 click    */ (event, el) => { forgetPasscode(); },
  /* 238 click    */ (event, el) => { chimeSet('tick', true); },
  /* 239 click    */ (event, el) => { chimeSet('tick', false); },
  /* 240 click    */ (event, el) => { chimeSet('room', true); },
  /* 241 click    */ (event, el) => { chimeSet('room', false); },
  /* 242 click    */ (event, el) => { chimeSet('signal', true); },
  /* 243 click    */ (event, el) => { chimeSet('signal', false); },
  /* 244 click    */ (event, el) => { chimeSet('personal', true); },
  /* 245 click    */ (event, el) => { chimeSet('personal', false); },
];

/** Attach every handler declared in the markup.
 *
 * One listener per element, not a delegated one at the document: an inline
 * attribute fires for its own element as the event bubbles through it, and
 * binding the same way keeps that identical. A delegated listener would
 * also fire for descendants and would be silenced by any stopPropagation
 * further down — small differences, but 230 of them at once is not where
 * to find out.
 *
 * Idempotent: an element already bound is skipped, so this is safe to call
 * again if markup is ever re-inserted.
 * @param {ParentNode} [root]
 */
function bindHandlers(root) {
  const scope = root || document;
  for (const ev of ['click', 'change', 'input', 'submit', 'keydown', 'blur', 'focus']) {
    const attr = 'data-on-' + ev;
    for (const el of scope.querySelectorAll('[' + attr + ']')) {
      const key = '__bound_' + ev;
      if (el[key]) continue;
      const fn = UI_HANDLERS[Number(el.getAttribute(attr))];
      if (!fn) continue;
      el[key] = true;
      el.addEventListener(ev, (event) => fn(event, el));
    }
  }
}

if (typeof window !== 'undefined') {
  window.bindHandlers = bindHandlers;
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => bindHandlers());
  } else {
    bindHandlers();
  }
}
