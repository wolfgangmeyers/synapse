import type { ReactElement } from "react";
import type { ImageInput } from "../domain/images";

export interface ImagePreviewProps { images: ImageInput[]; onRemove?: (index: number) => void }
export function ImagePreview({ images, onRemove }: ImagePreviewProps): ReactElement | null { if (images.length === 0) return null; return <div className="image-preview" aria-label="Attached images">{images.map((image, index) => <div className="image-card" key={`${image.name ?? "image"}-${index}`}><img src={image.dataUrl} alt={image.name ?? `Image ${index + 1}`} /><small title={image.name}>{image.name ?? "image.jpg"}</small><small>{Math.ceil(image.sizeBytes / 1024)} KiB</small>{onRemove ? <button type="button" onClick={() => onRemove(index)} aria-label={`Remove ${image.name ?? `image ${index + 1}`}`}>×</button> : null}</div>)}</div>; }
