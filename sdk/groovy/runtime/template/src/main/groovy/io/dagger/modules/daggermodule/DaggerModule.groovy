package io.dagger.modules.daggermoduleplaceholder

import io.dagger.client.Container
import io.dagger.client.Directory
import io.dagger.groovy.annotation.Object
import io.dagger.groovy.annotation.Function
import static io.dagger.client.Dagger.dag

/** DaggerModule main object */
@Object
class DaggerModule {
    /** Returns a container that echoes whatever string argument is provided */
    @Function
    Container containerEcho(String stringArg) {
        dag().container().from('alpine:latest').withExec(['echo', stringArg])
    }

    /** Returns lines that match a pattern in the files of the provided Directory */
    @Function
    String grepDir(Directory directoryArg, String pattern) {
        dag().container()
            .from('alpine:latest')
            .withMountedDirectory('/mnt', directoryArg)
            .withWorkdir('/mnt')
            .withExec(['grep', '-R', pattern, '.'])
            .stdout()
    }
}
