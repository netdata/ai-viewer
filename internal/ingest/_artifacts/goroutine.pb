goroutine profile: total 24
5 @ 0x48abae 0x41a9ae 0x41a4d2 0x99ef1b 0x99e47c 0x492521
#	0x99ef1a	main.runAdapter+0x29a				/home/costa/src/ai-viewer.git/cmd/ai-viewer-ingest/sources.go:475
#	0x99e47b	main.startSourceWithFactoryLookup.func1+0xbb	/home/costa/src/ai-viewer.git/cmd/ai-viewer-ingest/sources.go:361

3 @ 0x48abae 0x467385 0x5104f5 0x51388d 0x5137de 0x511642 0x51372c 0x578a65 0x57b565 0x57b4a5 0x57aac5 0x57a5a8 0x579fb8 0x56df35 0x492521
#	0x5104f4	database/sql.(*DB).conn+0x414									/usr/local/go/src/database/sql/sql.go:1369
#	0x51388c	database/sql.(*DB).begin+0x2c									/usr/local/go/src/database/sql/sql.go:1891
#	0x5137dd	database/sql.(*DB).BeginTx.func1+0x3d								/usr/local/go/src/database/sql/sql.go:1874
#	0x511641	database/sql.(*DB).retry+0x41									/usr/local/go/src/database/sql/sql.go:1576
#	0x51372b	database/sql.(*DB).BeginTx+0x6b									/usr/local/go/src/database/sql/sql.go:1873
#	0x578a64	github.com/netdata/ai-viewer/internal/ingest.(*worker).refreshRollupsOnly+0x64			/home/costa/src/ai-viewer.git/internal/ingest/worker.go:155
#	0x57b564	github.com/netdata/ai-viewer/internal/ingest.(*workerRuntime).idleRefreshWithWriteContext+0x64	/home/costa/src/ai-viewer.git/internal/ingest/worker_runtime.go:207
#	0x57b4a4	github.com/netdata/ai-viewer/internal/ingest.(*workerRuntime).idleRefresh+0x84			/home/costa/src/ai-viewer.git/internal/ingest/worker_runtime.go:200
#	0x57aac4	github.com/netdata/ai-viewer/internal/ingest.(*workerRuntime).handleTimer+0x44			/home/costa/src/ai-viewer.git/internal/ingest/worker_runtime.go:106
#	0x57a5a7	github.com/netdata/ai-viewer/internal/ingest.(*workerRuntime).run+0x107				/home/costa/src/ai-viewer.git/internal/ingest/worker_runtime.go:65
#	0x579fb7	github.com/netdata/ai-viewer/internal/ingest.(*worker).run+0x57					/home/costa/src/ai-viewer.git/internal/ingest/worker_runtime.go:23
#	0x56df34	github.com/netdata/ai-viewer/internal/ingest.(*Ingester).Submit.func1+0x54			/home/costa/src/ai-viewer.git/internal/ingest/ingester.go:323

3 @ 0x48abae 0x467385 0x57b9d1 0x492521
#	0x57b9d0	github.com/netdata/ai-viewer/internal/ingest.detachedWriteContext.func1+0xb0	/home/costa/src/ai-viewer.git/internal/ingest/worker_runtime.go:223

2 @ 0x48abae 0x467385 0x57a550 0x579fb8 0x56df35 0x492521
#	0x57a54f	github.com/netdata/ai-viewer/internal/ingest.(*workerRuntime).run+0xaf		/home/costa/src/ai-viewer.git/internal/ingest/worker_runtime.go:54
#	0x579fb7	github.com/netdata/ai-viewer/internal/ingest.(*worker).run+0x57			/home/costa/src/ai-viewer.git/internal/ingest/worker_runtime.go:23
#	0x56df34	github.com/netdata/ai-viewer/internal/ingest.(*Ingester).Submit.func1+0x54	/home/costa/src/ai-viewer.git/internal/ingest/ingester.go:323

1 @ 0x420e29 0x48ce18 0x5a7c33 0x492521
#	0x48ce17	os/signal.signal_recv+0x97	/usr/local/go/src/runtime/sigqueue.go:152
#	0x5a7c32	os/signal.loop+0x12		/usr/local/go/src/os/signal/signal_unix.go:23

1 @ 0x449171 0x4899dd 0x8f4751 0x8f4585 0x8f1409 0x90b6aa 0x90c13a 0x8ce469 0x8d0207 0x8d666e 0x8cc8d0 0x492521
#	0x8f4750	runtime/pprof.writeRuntimeProfile+0xb0	/usr/local/go/src/runtime/pprof/pprof.go:851
#	0x8f4584	runtime/pprof.writeGoroutine+0x44	/usr/local/go/src/runtime/pprof/pprof.go:784
#	0x8f1408	runtime/pprof.(*Profile).WriteTo+0x148	/usr/local/go/src/runtime/pprof/pprof.go:408
#	0x90b6a9	net/http/pprof.handler.ServeHTTP+0x529	/usr/local/go/src/net/http/pprof/pprof.go:273
#	0x90c139	net/http/pprof.Index+0xd9		/usr/local/go/src/net/http/pprof/pprof.go:397
#	0x8ce468	net/http.HandlerFunc.ServeHTTP+0x28	/usr/local/go/src/net/http/server.go:2286
#	0x8d0206	net/http.(*ServeMux).ServeHTTP+0x1c6	/usr/local/go/src/net/http/server.go:2828
#	0x8d666d	net/http.serverHandler.ServeHTTP+0x8d	/usr/local/go/src/net/http/server.go:3311
#	0x8cc8cf	net/http.(*conn).serve+0x64f		/usr/local/go/src/net/http/server.go:2073

1 @ 0x48abae 0x41a9ae 0x41a4d2 0x513de9 0x492521
#	0x513de8	database/sql.(*Tx).awaitDone+0x28	/usr/local/go/src/database/sql/sql.go:2212

1 @ 0x48abae 0x41a9ae 0x41a4d2 0x999a7c 0x998554 0x453bb5 0x492521
#	0x999a7b	main.run+0x14fb		/home/costa/src/ai-viewer.git/cmd/ai-viewer-ingest/main.go:280
#	0x998553	main.main+0x53		/home/costa/src/ai-viewer.git/cmd/ai-viewer-ingest/main.go:74
#	0x453bb4	runtime.main+0x2d4	/usr/local/go/src/runtime/proc.go:290

1 @ 0x48abae 0x44c657 0x489d85 0x4e0b27 0x4e214c 0x4e213a 0x5c1929 0x5d165b 0x5d0a50 0x8d15ac 0x8d1192 0x99a025 0x492521
#	0x489d84	internal/poll.runtime_pollWait+0x84		/usr/local/go/src/runtime/netpoll.go:351
#	0x4e0b26	internal/poll.(*pollDesc).wait+0x26		/usr/local/go/src/internal/poll/fd_poll_runtime.go:84
#	0x4e214b	internal/poll.(*pollDesc).waitRead+0x28b	/usr/local/go/src/internal/poll/fd_poll_runtime.go:89
#	0x4e2139	internal/poll.(*FD).Accept+0x279		/usr/local/go/src/internal/poll/fd_unix.go:613
#	0x5c1928	net.(*netFD).accept+0x28			/usr/local/go/src/net/fd_unix.go:150
#	0x5d165a	net.(*TCPListener).accept+0x1a			/usr/local/go/src/net/tcpsock_posix.go:159
#	0x5d0a4f	net.(*TCPListener).Accept+0x2f			/usr/local/go/src/net/tcpsock.go:387
#	0x8d15ab	net/http.(*Server).Serve+0x30b			/usr/local/go/src/net/http/server.go:3434
#	0x8d1191	net/http.(*Server).ListenAndServe+0x71		/usr/local/go/src/net/http/server.go:3360
#	0x99a024	main.run.func1+0x124				/home/costa/src/ai-viewer.git/cmd/ai-viewer-ingest/main.go:144

1 @ 0x48abae 0x467385 0x50fc09 0x492521
#	0x50fc08	database/sql.(*DB).connectionOpener+0x88	/usr/local/go/src/database/sql/sql.go:1261

1 @ 0x48abae 0x467385 0x5104f5 0x51388d 0x5137de 0x511642 0x51372c 0x569587 0x568bc6 0x5684a6 0x56e25d 0x999cd9 0x492521
#	0x5104f4	database/sql.(*DB).conn+0x414								/usr/local/go/src/database/sql/sql.go:1369
#	0x51388c	database/sql.(*DB).begin+0x2c								/usr/local/go/src/database/sql/sql.go:1891
#	0x5137dd	database/sql.(*DB).BeginTx.func1+0x3d							/usr/local/go/src/database/sql/sql.go:1874
#	0x511641	database/sql.(*DB).retry+0x41								/usr/local/go/src/database/sql/sql.go:1576
#	0x51372b	database/sql.(*DB).BeginTx+0x6b								/usr/local/go/src/database/sql/sql.go:1873
#	0x569586	github.com/netdata/ai-viewer/internal/ingest.insertFTSOpsBatch+0x66			/home/costa/src/ai-viewer.git/internal/ingest/fts_backfill.go:189
#	0x568bc5	github.com/netdata/ai-viewer/internal/ingest.backfillFTSOps+0x85			/home/costa/src/ai-viewer.git/internal/ingest/fts_backfill.go:138
#	0x5684a5	github.com/netdata/ai-viewer/internal/ingest.BackfillFTS+0xa5				/home/costa/src/ai-viewer.git/internal/ingest/fts_backfill.go:59
#	0x56e25c	github.com/netdata/ai-viewer/internal/ingest.(*Ingester).BackfillReadModels+0x15c	/home/costa/src/ai-viewer.git/internal/ingest/ingester.go:405
#	0x999cd8	main.run.func4+0xd8									/home/costa/src/ai-viewer.git/cmd/ai-viewer-ingest/main.go:261

1 @ 0x48abae 0x467385 0x5a771c 0x492521
#	0x5a771b	os/signal.NotifyContext.func1+0x7b	/usr/local/go/src/os/signal/signal.go:292

1 @ 0x48abae 0x467385 0x7e61bf 0x492521
#	0x7e61be	modernc.org/sqlite.interruptOnDone.func1+0x7e	/home/costa/go/pkg/mod/modernc.org/sqlite@v1.52.0/sqlite.go:94

1 @ 0x4a63a5 0x4a4e58 0x4e159d 0x4e1585 0x4e14c1 0x5c09e5 0x5c9bc5 0x8c6593 0x492521
#	0x4a63a4	syscall.Syscall+0x24				/usr/local/go/src/syscall/syscall_linux.go:74
#	0x4a4e57	syscall.read+0x37				/usr/local/go/src/syscall/zsyscall_linux_amd64.go:736
#	0x4e159c	syscall.Read+0x21c				/usr/local/go/src/syscall/syscall_unix.go:183
#	0x4e1584	internal/poll.ignoringEINTRIO+0x204		/usr/local/go/src/internal/poll/fd_unix.go:738
#	0x4e14c0	internal/poll.(*FD).Read+0x140			/usr/local/go/src/internal/poll/fd_unix.go:161
#	0x5c09e4	net.(*netFD).Read+0x24				/usr/local/go/src/net/fd_posix.go:68
#	0x5c9bc4	net.(*conn).Read+0x44				/usr/local/go/src/net/net.go:196
#	0x8c6592	net/http.(*connReader).backgroundRead+0x32	/usr/local/go/src/net/http/server.go:702

1 @ 0x770b79 0x77354e 0x772c85 0x773614 0x778e25 0x7798ae 0x692635 0x67c66c 0x67c9eb 0x7e1111 0x7eae65 0x7e407c 0x7e45c7 0x50beb7 0x51353c 0x517af1 0x512d77 0x5148e5 0x5717e8 0x571453 0x5715e2 0x5712d9 0x57113a 0x56d8f5 0x492521
#	0x770b78	modernc.org/sqlite/lib._jsonBlobAppendNode+0x158					/home/costa/go/pkg/mod/modernc.org/sqlite@v1.52.0/lib/sqlite_linux_amd64.go:168046
#	0x77354d	modernc.org/sqlite/lib._jsonTranslateTextToBlob+0x198d					/home/costa/go/pkg/mod/modernc.org/sqlite@v1.52.0/lib/sqlite_linux_amd64.go:168853
#	0x772c84	modernc.org/sqlite/lib._jsonTranslateTextToBlob+0x10c4					/home/costa/go/pkg/mod/modernc.org/sqlite@v1.52.0/lib/sqlite_linux_amd64.go:168603
#	0x773613	modernc.org/sqlite/lib._jsonConvertTextToBlob+0x33					/home/costa/go/pkg/mod/modernc.org/sqlite@v1.52.0/lib/sqlite_linux_amd64.go:169167
#	0x778e24	modernc.org/sqlite/lib._jsonParseFuncArg+0x1a4						/home/costa/go/pkg/mod/modernc.org/sqlite@v1.52.0/lib/sqlite_linux_amd64.go:171201
#	0x7798ad	modernc.org/sqlite/lib._jsonExtractFunc+0xad						/home/costa/go/pkg/mod/modernc.org/sqlite@v1.52.0/lib/sqlite_linux_amd64.go:171454
#	0x692634	modernc.org/sqlite/lib._sqlite3VdbeExec+0x10e14						/home/costa/go/pkg/mod/modernc.org/sqlite@v1.52.0/lib/sqlite_linux_amd64.go:71508
#	0x67c66b	modernc.org/sqlite/lib._sqlite3Step+0x6b						/home/costa/go/pkg/mod/modernc.org/sqlite@v1.52.0/lib/sqlite_linux_amd64.go:61716
#	0x67c9ea	modernc.org/sqlite/lib.Xsqlite3_step+0xaa						/home/costa/go/pkg/mod/modernc.org/sqlite@v1.52.0/lib/sqlite_linux_amd64.go:61779
#	0x7e1110	modernc.org/sqlite.(*conn).step+0x30							/home/costa/go/pkg/mod/modernc.org/sqlite@v1.52.0/conn.go:381
#	0x7eae64	modernc.org/sqlite.(*stmt).query+0x224							/home/costa/go/pkg/mod/modernc.org/sqlite@v1.52.0/stmt.go:337
#	0x7e407b	modernc.org/sqlite.(*conn).query+0x7b							/home/costa/go/pkg/mod/modernc.org/sqlite@v1.52.0/conn.go:1024
#	0x7e45c6	modernc.org/sqlite.(*conn).QueryContext+0x26						/home/costa/go/pkg/mod/modernc.org/sqlite@v1.52.0/conn.go:1248
#	0x50beb6	database/sql.ctxDriverQuery+0xd6							/usr/local/go/src/database/sql/ctxutil.go:48
#	0x51353b	database/sql.(*DB).queryDC.func1+0x15b							/usr/local/go/src/database/sql/sql.go:1786
#	0x517af0	database/sql.withLock+0x70								/usr/local/go/src/database/sql/sql.go:3572
#	0x512d76	database/sql.(*DB).queryDC+0x1b6							/usr/local/go/src/database/sql/sql.go:1781
#	0x5148e4	database/sql.(*Tx).QueryContext+0xc4							/usr/local/go/src/database/sql/sql.go:2535
#	0x5717e7	github.com/netdata/ai-viewer/internal/ingest.(*resolver).linkParents+0x67		/home/costa/src/ai-viewer.git/internal/ingest/resolver.go:166
#	0x571452	github.com/netdata/ai-viewer/internal/ingest.(*resolver).linkOrphans.func1+0x112	/home/costa/src/ai-viewer.git/internal/ingest/resolver.go:102
#	0x5715e1	github.com/netdata/ai-viewer/internal/ingest.(*resolver).runResolverTx+0x121		/home/costa/src/ai-viewer.git/internal/ingest/resolver.go:145
#	0x5712d8	github.com/netdata/ai-viewer/internal/ingest.(*resolver).linkOrphans+0x38		/home/costa/src/ai-viewer.git/internal/ingest/resolver.go:99
#	0x571139	github.com/netdata/ai-viewer/internal/ingest.(*resolver).loop+0x119			/home/costa/src/ai-viewer.git/internal/ingest/resolver.go:58
#	0x56d8f4	github.com/netdata/ai-viewer/internal/ingest.(*Ingester).Start.func1+0x54		/home/costa/src/ai-viewer.git/internal/ingest/ingester.go:277

