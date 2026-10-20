/**
 * Upload-format helpers for the knowledge base creation form.
 *
 * A .txt file is read as text and capped by character count; a .pdf is read as
 * bytes and shipped base64, because its figures have to reach the server intact
 * for a vision model to read them. Keeping the distinction here — rather than
 * inline in the form — is what makes it testable without mounting the modal.
 */

export const KB_FORMAT_TEXT = 'text';
export const KB_FORMAT_PDF = 'pdf';

/** Matches the server-side stored-payload guard once base64 inflation is applied. */
export const MAX_UPLOAD_BYTES = 5 * 1024 * 1024;

/**
 * A browser does not always report a MIME type (drag-and-drop from some file
 * managers gives an empty string), so the extension is a necessary fallback.
 */
export const isPdfFile = (file: File): boolean => file.type === 'application/pdf' || file.name.toLowerCase().endsWith('.pdf');

export const isTextFile = (file: File): boolean => file.type === 'text/plain' || file.name.toLowerCase().endsWith('.txt');

/**
 * Encode a file's bytes as base64.
 *
 * Chunked because String.fromCharCode takes its input as arguments: spreading a
 * multi-megabyte buffer in one call overflows the argument limit and throws on
 * exactly the large PDFs this feature exists for.
 */
export const arrayBufferToBase64 = (buffer: ArrayBuffer): string => {
  const bytes = new Uint8Array(buffer);
  const chunkSize = 0x8000;
  let binary = '';
  for (let i = 0; i < bytes.length; i += chunkSize) {
    binary += String.fromCharCode.apply(null, Array.from(bytes.subarray(i, i + chunkSize)));
  }
  return btoa(binary);
};
