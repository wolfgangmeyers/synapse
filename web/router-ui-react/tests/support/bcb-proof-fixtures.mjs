const CLIPBOARD_PASTE_HOOK = '[data-testid="clipboard-paste-target"]';
const CLIPBOARD_IMAGE = Object.freeze({
  name: 'clipboard.png',
  type: 'image/png',
  dataUrl: 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=',
});

/** Return JavaScript for BCB to dispatch one real image paste event. */
export function clipboardPasteScript(image = CLIPBOARD_IMAGE) {
  return `(() => {
    const target = document.querySelector(${JSON.stringify(CLIPBOARD_PASTE_HOOK)});
    if (!(target instanceof HTMLTextAreaElement)) throw new Error('Clipboard paste target is missing');
    const bytes = Uint8Array.from(atob(${JSON.stringify(image.dataUrl.split(',')[1])}), (character) => character.charCodeAt(0));
    const file = new File([bytes], ${JSON.stringify(image.name)}, { type: ${JSON.stringify(image.type)} });
    const transfer = new DataTransfer();
    transfer.items.add(file);
    target.dispatchEvent(new ClipboardEvent('paste', { bubbles: true, clipboardData: transfer }));
    return { target: ${JSON.stringify(CLIPBOARD_PASTE_HOOK)}, file: file.name };
  })()`;
}
