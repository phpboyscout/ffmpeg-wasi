// ffmpeg-wasi — shared metadata glue (tags, disposition) for probe + process.
// SPDX-License-Identifier: MIT
#ifndef FFMPEG_WASI_META_H
#define FFMPEG_WASI_META_H

#include <libavutil/dict.h>

#include "third_party/cJSON/cJSON.h"

// meta_add_tags adds a "tags" object under `parent` built from the AVDictionary
// `m` (container or per-stream metadata). No-op when `m` is empty (spec 0020).
void meta_add_tags(cJSON *parent, const AVDictionary *m);

// meta_add_disposition adds a "disposition" array under `parent` naming each set
// AV_DISPOSITION_* flag (default/forced/attached_pic/…). Always added (possibly
// empty) so a consumer can read it uniformly.
void meta_add_disposition(cJSON *parent, int disposition);

// meta_apply_tags sets every string entry of the `tags` JSON object into the
// AVDictionary `*dst` (an output's container or per-stream metadata).
void meta_apply_tags(AVDictionary **dst, const cJSON *tags);

// meta_disposition_from_json ORs the AV_DISPOSITION_* flags named in the `arr`
// JSON array (e.g. ["default","attached_pic"]); unknown names are warned and
// skipped. Returns the bitfield (0 for a null/empty array).
int meta_disposition_from_json(const cJSON *arr);

#endif
