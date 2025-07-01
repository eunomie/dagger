package io.dagger.modules.myjavamodule;

import static io.dagger.client.Dagger.dag;

import io.dagger.client.Container;
import io.dagger.client.exception.DaggerQueryException;
import io.dagger.module.annotation.Function;
import io.dagger.module.annotation.Object;
import java.util.List;
import java.util.concurrent.ExecutionException;

/** MyJavaModule main object */
@Object
public class MyJavaModule {
  /** Returns a container that echoes whatever string argument is provided */
  @Function
  public Container containerEcho(String stringArg) {
    return base().withExec(List.of("echo", stringArg));
  }

  @Function
  public String print(String stringArg)
      throws ExecutionException, DaggerQueryException, InterruptedException {
    return dag().glow().displayMarkdown(dag().myJavaModule().containerEcho(stringArg).stdout());
    //    return dag().glow().displayMarkdown(containerEcho(stringArg).stdout());
  }

  @Function
  public Container base() {
    return dag().container().from("alpine:latest");
  }
}
