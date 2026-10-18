//go:build cgo && (sqlite_fts5 || fts5)

package wcdbapi

/*
// The compatibility tokenizer mirrors the public WCDB verbatim rules needed by
// WeChat's `disable_pinyin` message index: Han characters are emitted one by
// one, while other text is delegated to SQLite's Porter tokenizer.

#include <stdlib.h>
#include <string.h>

typedef struct sqlite3 sqlite3;
typedef struct sqlite3_stmt sqlite3_stmt;
typedef struct sqlite3_mutex sqlite3_mutex;
typedef struct Fts5Tokenizer Fts5Tokenizer;
typedef struct fts5_api fts5_api;
typedef struct fts5_tokenizer fts5_tokenizer;

struct fts5_tokenizer {
	int (*xCreate)(void*, const char **azArg, int nArg, Fts5Tokenizer **ppOut);
	void (*xDelete)(Fts5Tokenizer*);
	int (*xTokenize)(
		Fts5Tokenizer*,
		void *pCtx,
		int flags,
		const char *pText,
		int nText,
		int (*xToken)(
			void *pCtx,
			int tflags,
			const char *pToken,
			int nToken,
			int iStart,
			int iEnd
		)
	);
};

struct fts5_api {
	int iVersion;
	int (*xCreateTokenizer)(
		fts5_api *pApi,
		const char *zName,
		void *pContext,
		fts5_tokenizer *pTokenizer,
		void (*xDestroy)(void*)
	);
	int (*xFindTokenizer)(
		fts5_api *pApi,
		const char *zName,
		void **ppContext,
		fts5_tokenizer *pTokenizer
	);
	int (*xCreateFunction)(fts5_api*, const char*, void*, void*, void(*)(void*));
};

extern int sqlite3_auto_extension(void(*xEntryPoint)(void));
extern int sqlite3_prepare_v2(sqlite3*, const char*, int, sqlite3_stmt**, const char**);
extern int sqlite3_bind_pointer(sqlite3_stmt*, int, void*, const char*, void(*)(void*));
extern int sqlite3_step(sqlite3_stmt*);
extern int sqlite3_finalize(sqlite3_stmt*);
extern void *sqlite3_malloc(int);
extern void sqlite3_free(void*);
extern sqlite3_mutex *sqlite3_mutex_alloc(int);
extern void sqlite3_mutex_enter(sqlite3_mutex*);
extern void sqlite3_mutex_leave(sqlite3_mutex*);

#define CHATLOG_SQLITE_OK 0
#define CHATLOG_SQLITE_ERROR 1
#define CHATLOG_SQLITE_NOMEM 7
#define CHATLOG_SQLITE_MUTEX_STATIC_APP1 8

typedef struct {
	void *porter_context;
	fts5_tokenizer porter;
} chatlog_mmfts_config;

typedef struct {
	chatlog_mmfts_config *config;
	Fts5Tokenizer *porter;
} chatlog_mmfts_tokenizer;

typedef struct {
	void *context;
	int offset;
	int (*token)(
		void *pCtx,
		int tflags,
		const char *pToken,
		int nToken,
		int iStart,
		int iEnd
	);
} chatlog_token_callback;

static int chatlog_adjusted_token(
	void *raw,
	int flags,
	const char *token,
	int token_length,
	int start,
	int end
) {
	chatlog_token_callback *callback = (chatlog_token_callback*) raw;
	return callback->token(
		callback->context,
		flags,
		token,
		token_length,
		start + callback->offset,
		end + callback->offset
	);
}

static int chatlog_utf8_codepoint(const unsigned char *text, int length, int *width) {
	unsigned char first;
	int codepoint;
	if (length <= 0) {
		*width = 0;
		return -1;
	}
	first = text[0];
	if (first < 0x80) {
		*width = 1;
		return first;
	}
	if ((first & 0xE0) == 0xC0 && length >= 2 && (text[1] & 0xC0) == 0x80) {
		codepoint = ((first & 0x1F) << 6) | (text[1] & 0x3F);
		if (codepoint >= 0x80) {
			*width = 2;
			return codepoint;
		}
	}
	if ((first & 0xF0) == 0xE0 && length >= 3
		&& (text[1] & 0xC0) == 0x80 && (text[2] & 0xC0) == 0x80) {
		codepoint = ((first & 0x0F) << 12)
			| ((text[1] & 0x3F) << 6)
			| (text[2] & 0x3F);
		if (codepoint >= 0x800 && !(codepoint >= 0xD800 && codepoint <= 0xDFFF)) {
			*width = 3;
			return codepoint;
		}
	}
	if ((first & 0xF8) == 0xF0 && length >= 4
		&& (text[1] & 0xC0) == 0x80 && (text[2] & 0xC0) == 0x80
		&& (text[3] & 0xC0) == 0x80) {
		codepoint = ((first & 0x07) << 18)
			| ((text[1] & 0x3F) << 12)
			| ((text[2] & 0x3F) << 6)
			| (text[3] & 0x3F);
		if (codepoint >= 0x10000 && codepoint <= 0x10FFFF) {
			*width = 4;
			return codepoint;
		}
	}
	*width = 1;
	return -1;
}

static int chatlog_is_han(int codepoint) {
	return (codepoint >= 0x3400 && codepoint <= 0x4DBF)
		|| (codepoint >= 0x4E00 && codepoint <= 0x9FFF)
		|| (codepoint >= 0xF900 && codepoint <= 0xFAFF)
		|| (codepoint >= 0x20000 && codepoint <= 0x2FA1F)
		|| (codepoint >= 0x30000 && codepoint <= 0x323AF);
}

static int chatlog_mmfts_create(
	void *raw_config,
	const char **arguments,
	int argument_count,
	Fts5Tokenizer **output
) {
	chatlog_mmfts_config *config = (chatlog_mmfts_config*) raw_config;
	chatlog_mmfts_tokenizer *tokenizer;
	int result;
	(void) arguments;
	(void) argument_count;
	tokenizer = (chatlog_mmfts_tokenizer*) sqlite3_malloc(sizeof(*tokenizer));
	if (tokenizer == NULL) {
		return CHATLOG_SQLITE_NOMEM;
	}
	memset(tokenizer, 0, sizeof(*tokenizer));
	tokenizer->config = config;
	result = config->porter.xCreate(config->porter_context, NULL, 0, &tokenizer->porter);
	if (result != CHATLOG_SQLITE_OK) {
		sqlite3_free(tokenizer);
		return result;
	}
	*output = (Fts5Tokenizer*) tokenizer;
	return CHATLOG_SQLITE_OK;
}

static void chatlog_mmfts_delete(Fts5Tokenizer *raw_tokenizer) {
	chatlog_mmfts_tokenizer *tokenizer = (chatlog_mmfts_tokenizer*) raw_tokenizer;
	if (tokenizer == NULL) {
		return;
	}
	if (tokenizer->porter != NULL) {
		tokenizer->config->porter.xDelete(tokenizer->porter);
	}
	sqlite3_free(tokenizer);
}

static int chatlog_mmfts_tokenize(
	Fts5Tokenizer *raw_tokenizer,
	void *context,
	int flags,
	const char *text,
	int text_length,
	int (*token)(
		void *pCtx,
		int tflags,
		const char *pToken,
		int nToken,
		int iStart,
		int iEnd
	)
) {
	chatlog_mmfts_tokenizer *tokenizer = (chatlog_mmfts_tokenizer*) raw_tokenizer;
	int cursor = 0;
	if (text == NULL || text_length <= 0) {
		return CHATLOG_SQLITE_OK;
	}
	while (cursor < text_length) {
		int width = 0;
		int codepoint = chatlog_utf8_codepoint(
			(const unsigned char*) text + cursor,
			text_length - cursor,
			&width
		);
		if (chatlog_is_han(codepoint)) {
			int result = token(context, 0, text + cursor, width, cursor, cursor + width);
			if (result != CHATLOG_SQLITE_OK) {
				return result;
			}
			cursor += width;
			continue;
		}

		{
			int start = cursor;
			chatlog_token_callback callback;
			while (cursor < text_length) {
				codepoint = chatlog_utf8_codepoint(
					(const unsigned char*) text + cursor,
					text_length - cursor,
					&width
				);
				if (chatlog_is_han(codepoint)) {
					break;
				}
				cursor += width;
			}
			callback.context = context;
			callback.offset = start;
			callback.token = token;
			{
				int result = tokenizer->config->porter.xTokenize(
					tokenizer->porter,
					&callback,
					flags,
					text + start,
					cursor - start,
					chatlog_adjusted_token
				);
				if (result != CHATLOG_SQLITE_OK) {
					return result;
				}
			}
		}
	}
	return CHATLOG_SQLITE_OK;
}

static fts5_api *chatlog_fts5_api(sqlite3 *database) {
	fts5_api *api = NULL;
	sqlite3_stmt *statement = NULL;
	if (sqlite3_prepare_v2(database, "SELECT fts5(?1)", -1, &statement, NULL)
		!= CHATLOG_SQLITE_OK) {
		return NULL;
	}
	sqlite3_bind_pointer(statement, 1, (void*) &api, "fts5_api_ptr", NULL);
	sqlite3_step(statement);
	sqlite3_finalize(statement);
	return api;
}

static void chatlog_mmfts_config_destroy(void *raw_config) {
	sqlite3_free(raw_config);
}

static int chatlog_mmfts_extension_init(sqlite3 *database, char **error, const void *sqlite_api) {
	fts5_api *api;
	sqlite3_mutex *mutex;
	void *existing_context = NULL;
	fts5_tokenizer existing;
	chatlog_mmfts_config *config;
	fts5_tokenizer module;
	int result;
	(void) error;
	(void) sqlite_api;

	// sqlite3_auto_extension may invoke this callback concurrently when the
	// dashboard, message scanners and OCR scanner open several read-only
	// shards at once. FTS5 tokenizer discovery/registration is not reliable
	// under those concurrent initializers and sporadically makes sqlite3_open
	// report "automatic extension loading failed". SQLite reserves three
	// static application mutexes for process-wide extension coordination.
	mutex = sqlite3_mutex_alloc(CHATLOG_SQLITE_MUTEX_STATIC_APP1);
	if (mutex != NULL) {
		sqlite3_mutex_enter(mutex);
	}

	api = chatlog_fts5_api(database);
	if (api == NULL) {
		// This connection does not expose FTS5. The tokenizer is optional for
		// ordinary message/session shards, so do not make sqlite3_open fail with
		// the opaque "automatic extension loading failed" error.
		result = CHATLOG_SQLITE_OK;
		goto done;
	}
	memset(&existing, 0, sizeof(existing));
	if (api->xFindTokenizer(
		api,
		"MMFtsTokenizer",
		&existing_context,
		&existing
	) == CHATLOG_SQLITE_OK) {
		result = CHATLOG_SQLITE_OK;
		goto done;
	}

	config = (chatlog_mmfts_config*) sqlite3_malloc(sizeof(*config));
	if (config == NULL) {
		result = CHATLOG_SQLITE_NOMEM;
		goto done;
	}
	memset(config, 0, sizeof(*config));
	result = api->xFindTokenizer(
		api,
		"porter",
		&config->porter_context,
		&config->porter
	);
	if (result != CHATLOG_SQLITE_OK) {
		sqlite3_free(config);
		// Keep non-FTS database connections usable. A database that actually
		// references MMFtsTokenizer will still report that precise schema error.
		result = CHATLOG_SQLITE_OK;
		goto done;
	}

	memset(&module, 0, sizeof(module));
	module.xCreate = chatlog_mmfts_create;
	module.xDelete = chatlog_mmfts_delete;
	module.xTokenize = chatlog_mmfts_tokenize;
	result = api->xCreateTokenizer(
		api,
		"MMFtsTokenizer",
		config,
		&module,
		chatlog_mmfts_config_destroy
	);
	if (result != CHATLOG_SQLITE_OK) {
		sqlite3_free(config);
		if (result != CHATLOG_SQLITE_NOMEM) {
			result = CHATLOG_SQLITE_OK;
		}
	}
done:
	if (mutex != NULL) {
		sqlite3_mutex_leave(mutex);
	}
	return result;
}

static int chatlog_register_mmfts_auto_extension(void) {
	return sqlite3_auto_extension((void(*)(void)) chatlog_mmfts_extension_init);
}
*/
import "C"

var mmftsTokenizerRegistrationCode = int(C.chatlog_register_mmfts_auto_extension())

// MMFTSTokenizerRegistrationCode exposes the sqlite3_auto_extension result for
// diagnostics. A zero value means new SQLite connections receive the
// compatibility tokenizer before their FTS5 schemas are parsed.
func MMFTSTokenizerRegistrationCode() int {
	return mmftsTokenizerRegistrationCode
}
